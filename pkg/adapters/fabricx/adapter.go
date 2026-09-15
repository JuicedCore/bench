package fabricx

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	fxcommon "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/protobuf/proto"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "fabricx"

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter implements adapters.PlatformAdapter for Fabric-X over its native gRPC
// path: broadcast to an Arma router, finality from the sidecar's deliver stream.
type Adapter struct {
	cfg    *Config
	signer *nsSigner
	bc     *routerSet
	dl     *deliverer

	log *slog.Logger

	mu        sync.Mutex
	waiters   map[string]chan blockOutcome
	resolved  map[string]blockOutcome
	lastPrune time.Time
}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) PlatformVersion() string { return platformName }

func (a *Adapter) MetricsEndpoint() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.MetricsEndpointURL
}

// CryptoInfo: application transactions carry an ECDSA-P256 endorsement over the
// namespace's ASN.1 encoding, verified by the committer against the policy
// registered for that namespace. Unlike the Fabric family there is no
// per-transaction endorsement round-trip to a peer - the client signs directly.
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote: "Fabric-X: client-signed ECDSA endorsement per namespace, verified by the committer " +
			"against the namespace policy registered in _meta at deploy time. No peer endorsement round-trip.",
	}
}

func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	a.log = ac.Log()
	cfg, err := configFromExtra(ac.Extra)
	if err != nil {
		return err
	}
	a.cfg = cfg

	if a.signer, err = loadNsSigner(cfg.SigningKeyPath); err != nil {
		return fmt.Errorf("%w (signing_key_path must be the key deploy/docker/fabricx/up.sh registered for namespace %q)", err, cfg.Namespace)
	}
	a.waiters = map[string]chan blockOutcome{}
	a.resolved = map[string]blockOutcome{}

	dctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	// Deliver first: a transaction broadcast before the stream is live would
	// never be observed and would time out for no visible reason.
	if a.dl, err = newDeliverer(dctx, cfg.DeliverEndpoint, cfg.ChannelID, a.onOutcomes, a.log); err != nil {
		return err
	}
	bctx, bcancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer bcancel()
	if a.bc, err = newRouterSet(bctx, cfg.BroadcastEndpoints, cfg.BroadcastStreams, cfg.AckTimeout, a.log); err != nil {
		a.dl.close()
		a.dl = nil
		return err
	}
	a.log.Info("setup complete", "routers", strings.Join(cfg.BroadcastEndpoints, ","), "deliver", cfg.DeliverEndpoint,
		"channel", cfg.ChannelID, "namespace", cfg.Namespace, "broadcast_streams", cfg.BroadcastStreams)
	return nil
}

func (a *Adapter) Teardown(context.Context) error {
	if a.bc != nil {
		a.bc.close()
		a.bc = nil
	}
	if a.dl != nil {
		a.dl.close()
		a.dl = nil
	}
	a.mu.Lock()
	a.waiters = map[string]chan blockOutcome{}
	a.resolved = map[string]blockOutcome{}
	a.mu.Unlock()
	return nil
}

// onOutcomes routes a decoded block's results to whoever is waiting, or parks
// them for a caller that has not arrived yet.
func (a *Adapter) onOutcomes(outs []blockOutcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range outs {
		if ch, ok := a.waiters[o.txID]; ok {
			select {
			case ch <- o:
			default:
			}
			continue
		}
		a.resolved[o.txID] = o
	}
	// Bound outcomes nobody waits for (other clients' transactions, or ours whose
	// submit failed but which committed anyway).
	if n := len(a.resolved); n > maxResolved && len(outs) > 0 && outs[0].observedAt.Sub(a.lastPrune) > 10*time.Second {
		now := outs[0].observedAt
		a.lastPrune = now
		for id, o := range a.resolved {
			if now.Sub(o.observedAt) > resolvedTTL {
				delete(a.resolved, id)
			}
		}
		a.log.Debug("pruned unclaimed finality results", "before", n, "after", len(a.resolved))
	}
}

const (
	maxResolved = 200_000
	resolvedTTL = 5 * time.Minute
)

// Submit builds, signs and broadcasts one transaction to every Arma router, and
// returns once the first router has acknowledged it. T2 is the moment that
// acknowledgement arrived: a router accepted the envelope and forwarded it for
// ordering, strictly before commit - the same point at which the Fabric
// gateway's Submit returns.
func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	if a.bc == nil {
		return nil, fmt.Errorf("fabricx: %w", adapters.ErrNotSetUp)
	}
	t1 := time.Now()

	txID, env, err := a.buildEnvelope(tx)
	if err != nil {
		return &adapters.SubmitResult{SubmitTime: t1}, fmt.Errorf("fabricx: build envelope: %w", err)
	}

	// Register before sending so the deliver stream can never resolve a
	// transaction we are not yet listening for.
	a.mu.Lock()
	if _, dup := a.waiters[txID]; !dup {
		a.waiters[txID] = make(chan blockOutcome, 1)
	}
	a.mu.Unlock()

	ackAt, err := a.bc.submit(ctx, env)
	if err != nil {
		// Not acknowledged, or refused by the router: it will not be ordered, so
		// stop listening for it and report the submit as failed.
		a.mu.Lock()
		delete(a.waiters, txID)
		delete(a.resolved, txID)
		a.mu.Unlock()
		return &adapters.SubmitResult{TxID: txID, SubmitTime: t1}, err
	}
	return &adapters.SubmitResult{TxID: txID, SubmitTime: t1, AckTime: ackAt}, nil
}

func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	if a.waiters == nil {
		return nil, fmt.Errorf("fabricx: %w", adapters.ErrNotSetUp)
	}
	a.mu.Lock()
	if o, ok := a.resolved[txID]; ok {
		delete(a.resolved, txID)
		delete(a.waiters, txID)
		a.mu.Unlock()
		return finality(o), nil
	}
	ch, ok := a.waiters[txID]
	if !ok {
		ch = make(chan blockOutcome, 1)
		a.waiters[txID] = ch
	}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.waiters, txID)
		delete(a.resolved, txID)
		a.mu.Unlock()
	}()

	var down <-chan struct{}
	if a.dl != nil {
		down = a.dl.down
		if err := a.dl.err(); err != nil {
			return nil, err
		}
	}
	select {
	case o := <-ch:
		return finality(o), nil
	case <-down:
		return nil, a.dl.err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("fabricx: %w: tx %s not seen on the deliver stream from %s within %s",
			adapters.ErrFinalityTimeout, txID, a.cfg.DeliverEndpoint, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// finality reports T3 as the instant the block was observed on the deliver
// stream. A non-COMMITTED status is a platform verdict, so it comes back as
// a terminal result with Valid=false rather than an error - that is what lets the
// failure breakdown separate a rejected transaction from an unreachable platform.
func finality(o blockOutcome) *adapters.FinalityResult {
	r := &adapters.FinalityResult{
		TxID:         o.txID,
		FinalityTime: o.observedAt,
		BlockNum:     o.blockNum,
		Valid:        o.valid,
	}
	if !o.valid {
		r.InvalidReason = fmt.Sprintf("fabricx status %s", committerpb.Status(o.code))
	}
	return r
}

// Query is not wired: Fabric-X's query service is a separate endpoint from the
// benchmark path, and nothing in the harness calls Query. Returns not-found so
// workload verification degrades visibly rather than reporting a wrong value.
func (a *Adapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return &adapters.QueryResult{Found: false}, nil
}

// buildEnvelope maps a normalized transaction onto a Fabric-X application
// transaction and wraps it for broadcast. The envelope shape mirrors upstream's
// own tx builder (loadgen/workload/tx_builder.go).
func (a *Adapter) buildEnvelope(tx *adapters.Transaction) (string, *fxcommon.Envelope, error) {
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	sigHeader := &fxcommon.SignatureHeader{Nonce: nonce}
	txID := protoutil.ComputeTxID(sigHeader.Nonce, sigHeader.Creator)

	ns, err := a.namespaceFor(tx)
	if err != nil {
		return "", nil, err
	}
	appTx := &applicationpb.Tx{Namespaces: []*applicationpb.TxNamespace{ns}}
	if err := a.signer.endorse(txID, appTx); err != nil {
		return "", nil, err
	}

	chanHeader := protoutil.MakeChannelHeader(fxcommon.HeaderType_MESSAGE, 0, a.cfg.ChannelID, 0)
	chanHeader.TxId = txID
	body, err := proto.Marshal(appTx)
	if err != nil {
		return "", nil, err
	}
	payload, err := proto.Marshal(&fxcommon.Payload{
		Header: protoutil.MakePayloadHeader(chanHeader, sigHeader),
		Data:   body,
	})
	if err != nil {
		return "", nil, err
	}
	return txID, &fxcommon.Envelope{Payload: payload}, nil
}

// namespaceFor maps the normalized workload onto Fabric-X read/write sets.
func (a *Adapter) namespaceFor(tx *adapters.Transaction) (*applicationpb.TxNamespace, error) {
	ns := &applicationpb.TxNamespace{NsId: a.cfg.Namespace, NsVersion: 0}

	switch tx.Kind {
	case adapters.TxRead:
		// The validator rejects read-only transactions outright
		// (MALFORMED_NO_WRITES), so a read must carry a write to be accepted at
		// all. A unique blind write is used rather than echoing the value back,
		// which would make concurrent reads of one key abort each other. This is
		// overhead no other platform pays and is disclosed in the run caveats -
		// see docs/workloads/mismatches.md.
		ns.ReadsOnly = []*applicationpb.Read{{Key: []byte(tx.Key)}}
		ns.BlindWrites = []*applicationpb.Write{{
			Key:   []byte(fmt.Sprintf("_r/%s/%d", tx.Key, tx.Seq)),
			Value: []byte{1},
		}}

	case adapters.TxTransfer:
		// Read-modify-write on both accounts. Values are the workload's, not a
		// computed balance: Fabric-X has no chaincode to evaluate a predicate,
		// so the transfer is modelled as a two-key RW set of the same shape the
		// other platforms produce.
		ns.ReadWrites = []*applicationpb.ReadWrite{
			{Key: []byte(tx.Key), Value: tx.Value},
			{Key: []byte(tx.DestKey), Value: tx.Value},
		}

	default: // TxWrite
		ns.BlindWrites = []*applicationpb.Write{{Key: []byte(tx.Key), Value: tx.Value}}
	}
	return ns, nil
}

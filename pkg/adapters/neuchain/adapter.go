// Package neuchain is the PlatformAdapter for NeuChain (ordering-free
// Execute-Validate, C++). PURE-GO client: ZeroMQ + protobuf + RSA-1024/SHA-256,
// no cgo, no native `user` binary. See the spike write-up in
// docs/platforms/neuchain-client-implementation.md and adr-002.
//
//	submit    -> ZMQ PUB  to <block-server>:5001, comm.UserRequest{payload, digest=sig}
//	finality  -> ZMQ REQ  to <block-server>:7003, tip_query / block_query polling;
//	             the poller decodes each block's hand-rolled result frames and
//	             resolves waiting transactions by digest.
package neuchain

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "neuchain"

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter implements adapters.PlatformAdapter for NeuChain.
type Adapter struct {
	cfg    *Config
	signer *signer
	tr     *zmqTransport

	pollCtx    context.Context
	pollCancel context.CancelFunc
	pollDone   chan struct{}

	mu       sync.Mutex
	waiters  map[string]chan resultFrame // hex(digest) -> signal
	resolved map[string]resultFrame      // hex(digest) -> frame, for finality seen before WaitForFinality
}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) PlatformVersion() string { return platformName }
func (a *Adapter) MetricsEndpoint() string { return "" }

// CryptoInfo: NeuChain verifies the user's RSA signature once on submit; there is
// no endorsement round. per_tx_endorsement_verify=false is the right flag for
// the cross-platform table (see neuchain-client-implementation.md §5).
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "RSA-1024-PKCS1v15",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: false,
		MSPNote:                "NeuChain EV: RSA-1024 user signature over the serialized TransactionPayload; no endorsement phase, ordering implicit via deterministic execution",
	}
}

func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	cfg, err := configFromExtra(ac.Extra)
	if err != nil {
		return err
	}
	a.cfg = cfg

	if a.signer, err = loadSigner(cfg.UserPrivKeyPath, cfg.KeyPassword); err != nil {
		return err
	}
	if a.tr, err = newTransport(context.Background(), cfg, a.signer.sign); err != nil {
		return err
	}

	a.waiters = map[string]chan resultFrame{}
	a.resolved = map[string]resultFrame{}

	// Verify the query path is alive (tip may legitimately be 0 pre-genesis).
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.pingTip(tctx); err != nil {
		a.tr.Close()
		return fmt.Errorf("neuchain: query endpoint unreachable: %w", err)
	}

	a.pollCtx, a.pollCancel = context.WithCancel(context.Background())
	a.pollDone = make(chan struct{})
	go a.poll()
	return nil
}

func (a *Adapter) pingTip(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { _, err := a.tr.tip(); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) Teardown(context.Context) error {
	if a.pollCancel != nil {
		a.pollCancel()
		<-a.pollDone
	}
	if a.tr != nil {
		a.tr.Close()
	}
	return nil
}

// Submit builds, signs and publishes one transaction. T2 = the moment the ZMQ
// send returns (NeuChain PUB is fire-and-forget - there is no ack).
func (a *Adapter) Submit(_ context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	t1 := time.Now()
	wire, sig, err := a.buildInvoke(tx)
	if err != nil {
		return &adapters.SubmitResult{SubmitTime: t1}, err
	}
	id := hex.EncodeToString(sig)

	// Register the waiter before publishing so the poller can never miss it.
	a.mu.Lock()
	if _, dup := a.waiters[id]; !dup {
		a.waiters[id] = make(chan resultFrame, 1)
	}
	a.mu.Unlock()

	if err := a.tr.publish(wire); err != nil {
		a.mu.Lock()
		delete(a.waiters, id)
		a.mu.Unlock()
		return &adapters.SubmitResult{TxID: id, SubmitTime: t1}, err
	}
	return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
}

// WaitForFinality blocks until the poller sees txID in a committed block.
func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	if f, ok := a.resolved[txID]; ok {
		delete(a.resolved, txID)
		a.mu.Unlock()
		return finality(txID, f), nil
	}
	ch, ok := a.waiters[txID]
	if !ok {
		ch = make(chan resultFrame, 1)
		a.waiters[txID] = ch
	}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.waiters, txID)
		delete(a.resolved, txID)
		a.mu.Unlock()
	}()

	select {
	case f := <-ch:
		return finality(txID, f), nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("neuchain: finality timeout for %s", txID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Query is a state read via NeuChain's query path. Not wired to a dedicated RPC
// here (the block-query path returns results, not point reads); returns
// not-found so workload verification degrades gracefully rather than lying.
func (a *Adapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return &adapters.QueryResult{Found: false}, nil
}

// poll tracks the chain tip and decodes each new block's result frames,
// resolving waiting transactions by digest.
func (a *Adapter) poll() {
	defer close(a.pollDone)
	next := a.cfg.StartBlock
	t := time.NewTicker(a.cfg.PollInterval)
	defer t.Stop()

	for {
		select {
		case <-a.pollCtx.Done():
			return
		case <-t.C:
		}

		tip, err := a.tr.tip()
		if err != nil || tip < next {
			continue
		}
		for n := next; n <= tip; n++ {
			frames, err := a.tr.block(n)
			if err != nil {
				break // retry this height next tick
			}
			for _, f := range frames {
				id := hex.EncodeToString(f.Digest)
				a.mu.Lock()
				if ch, ok := a.waiters[id]; ok {
					select {
					case ch <- f:
					default:
					}
				} else {
					a.resolved[id] = f
				}
				a.mu.Unlock()
			}
			next = n + 1
		}
	}
}

func finality(txID string, f resultFrame) *adapters.FinalityResult {
	return &adapters.FinalityResult{
		TxID:         txID,
		FinalityTime: time.Now(),
		BlockNum:     f.Epoch,
		Valid:        f.valid(),
	}
}

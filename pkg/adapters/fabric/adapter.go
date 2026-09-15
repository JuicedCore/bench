// Package fabric is the PlatformAdapter for Hyperledger Fabric (CFT / Raft and
// BFT / SmartBFT) and the base implementation reused by the Drunix adapter.
//
// It uses the Fabric Gateway SDK's fine-grained flow -
// NewProposal -> Endorse -> Submit -> Commit.Status - so that the acknowledge
// timestamp (T2, after the orderer accepts the broadcast) is distinct from the
// finality timestamp (T3, after the tx appears in a validated block). The
// one-shot contract.SubmitTransaction call is deliberately NOT used because it
// collapses T2 and T3. See docs/architecture/metrics-methodology.md and
// docs/platforms/fabric.md.
package fabric

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/juicedcore/bench/pkg/adapters"
)

func init() {
	adapters.Register("fabric-cft", func() adapters.PlatformAdapter { return &Adapter{name: "fabric-cft"} })
	adapters.Register("fabric-bft", func() adapters.PlatformAdapter { return &Adapter{name: "fabric-bft"} })
}

// Adapter implements adapters.PlatformAdapter for Fabric-family networks.
type Adapter struct {
	name string
	cfg  *Config

	conn     *grpc.ClientConn
	gw       *client.Gateway
	contract *client.Contract

	cp  *cpListener // block-event finality source; set by Setup for every platform
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*inflight
}

type inflight struct {
	commit  *client.Commit // nil for reads
	readTx  bool
	created time.Time
}

// NewWithConfig builds an adapter with an explicit config. Used by the Drunix
// adapter to reuse this implementation with different endpoints.
func NewWithConfig(name string, cfg *Config) *Adapter {
	cfg.PlatformName = name
	return &Adapter{name: name, cfg: cfg}
}

func (a *Adapter) Name() string { return a.name }

// PlatformVersion is best-effort; the concrete image/tag is recorded by the
// deploy scripts into the manifest's environment. Returns "" here so the engine
// keeps "unknown" unless the deploy layer injects it.
func (a *Adapter) PlatformVersion() string {
	if a.cfg != nil && a.cfg.PlatformName != "" {
		return a.cfg.PlatformName
	}
	return ""
}

// CryptoInfo declares Fabric's per-transaction signature verification so the
// EOV-vs-EV difference is disclosed in the manifest.
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote:                "X.509 MSP; endorsement signatures verified by every committing peer during VSCC",
	}
}

func (a *Adapter) MetricsEndpoint() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.MetricsEndpointURL
}

// Setup connects the Gateway SDK to the endorsing peer. On any failure it
// releases what it opened, so Teardown afterwards is a no-op.
func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) (err error) {
	a.log = ac.Log()
	if a.cfg == nil {
		c, err := configFromExtra(a.name, ac.Extra)
		if err != nil {
			return err
		}
		a.cfg = c
	}
	a.pending = map[string]*inflight{}
	defer func() {
		if err != nil {
			a.releaseAll()
		}
	}()

	id, err := loadIdentity(a.cfg.MSPID, a.cfg.CertPath)
	if err != nil {
		return fmt.Errorf("%s: identity: %w", a.name, err)
	}
	sign, err := loadSign(a.cfg.KeyPath)
	if err != nil {
		return fmt.Errorf("%s: signing key: %w", a.name, err)
	}
	conn, err := dial(a.cfg.EndorseEndpoint, a.cfg.TLSCACertPath, a.cfg.GatewayPeer)
	if err != nil {
		return fmt.Errorf("%s: gateway peer %s: %w", a.name, a.cfg.EndorseEndpoint, err)
	}
	a.conn = conn
	a.log.Debug("gateway connection configured", "endpoint", a.cfg.EndorseEndpoint, "tls_server_name", a.cfg.GatewayPeer,
		"msp_id", a.cfg.MSPID, "channel", a.cfg.Channel, "chaincode", a.cfg.Chaincode)

	gw, err := client.Connect(id,
		client.WithSign(sign),
		client.WithClientConnection(conn),
		client.WithEndorseTimeout(a.cfg.EndorseTimeout),
		client.WithSubmitTimeout(a.cfg.SubmitTimeout),
		client.WithCommitStatusTimeout(a.cfg.CommitStatusTimeout),
		client.WithEvaluateTimeout(a.cfg.EndorseTimeout),
	)
	if err != nil {
		return fmt.Errorf("%s: gateway connect: %w", a.name, err)
	}
	a.gw = gw
	a.contract = gw.GetNetwork(a.cfg.Channel).GetContract(a.cfg.Chaincode)

	// Finality always comes from a block event stream so T3 is stamped at
	// observation (see cpListener). Drunix: the Gateway is on the Lite Peer, which
	// never commits, so watch the separate Committing Peer. Otherwise the gateway
	// peer is the committing peer and its own stream is used.
	if !a.cfg.UseCommitPeerEvents {
		l, lerr := listenBlockEvents(gw, a.cfg.EndorseEndpoint, a.cfg.Channel, a.log)
		if lerr != nil {
			return fmt.Errorf("%s: block-event listener on %s: %w", a.name, a.cfg.EndorseEndpoint, lerr)
		}
		a.cp = l
	} else {
		cpSNI := a.cfg.CommitPeerGateway
		if cpSNI == "" {
			cpSNI = defaultCommitPeerSNI(a.cfg.GatewayPeer)
		}
		cp, cerr := startCPListener(a.cfg.CommitEndpoint, a.cfg.TLSCACertPath, cpSNI, a.cfg.Channel, id, sign, a.log)
		if cerr != nil {
			return fmt.Errorf("%s: commit-peer listener (tls server name %q): %w", a.name, cpSNI, cerr)
		}
		a.cp = cp
	}

	// Fail fast if the chaincode is not reachable. kvstore's Get returns an
	// empty value, not an error, for a missing key, so a healthy network answers
	// this without error.
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, perr := a.contract.EvaluateWithContext(pctx, a.cfg.FnGet, client.WithArguments("__healthcheck__")); perr != nil {
		if fatal, hint := classifyProbeError(perr); fatal {
			return fmt.Errorf("%s: chaincode %q on channel %q via %s is not usable: %s: %w",
				a.name, a.cfg.Chaincode, a.cfg.Channel, a.cfg.EndorseEndpoint, hint, perr)
		}
		a.log.Warn("health-check evaluate returned an error; continuing, but the first transactions may fail the same way",
			"fn", a.cfg.FnGet, "err", perr)
	}
	a.log.Info("setup complete", "gateway", a.cfg.EndorseEndpoint, "finality_from", a.cp.endpoint)
	return nil
}

// classifyProbeError decides whether the Setup health check failed because the
// network or chaincode is unusable (fatal, with a hint) or for a reason that may
// not affect writes.
func classifyProbeError(err error) (fatal bool, hint string) {
	msg := strings.ToLower(err.Error())
	switch status.Code(err) {
	case codes.Unavailable:
		if strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") || strings.Contains(msg, "tls") {
			return true, "TLS handshake failed - adapter.gateway_peer must match the peer certificate's name and tls_ca_cert_path must be this network's CA"
		}
		return true, "peer unreachable - is the network up (docker ps) and adapter.peer_endpoint right?"
	case codes.DeadlineExceeded:
		return true, "no answer within 10s - peer overloaded, or the chaincode container is not starting (docker logs dev-peer*)"
	case codes.Unauthenticated, codes.PermissionDenied:
		return true, "identity rejected - cert_path/key_path/msp_id must belong to this network (re-source connection.env after a redeploy)"
	}
	for _, s := range []string{"chaincode", "not found", "cannot find", "could not find", "does not exist", "channel"} {
		if strings.Contains(msg, s) && (strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "find")) {
			return true, "chaincode or channel not found - was the chaincode deployed (deploy/docker/<platform>/up.sh) and do adapter.channel / adapter.chaincode match?"
		}
	}
	return false, ""
}

// Teardown releases client resources. Safe after a failed Setup and when called
// more than once.
func (a *Adapter) Teardown(context.Context) error {
	return a.releaseAll()
}

func (a *Adapter) releaseAll() error {
	a.cp.close()
	a.cp = nil
	if a.gw != nil {
		a.gw.Close()
		a.gw = nil
	}
	a.contract = nil
	if a.conn != nil {
		err := a.conn.Close()
		a.conn = nil
		if err != nil {
			return fmt.Errorf("%s: close gateway connection: %w", a.name, err)
		}
	}
	return nil
}

// defaultCommitPeerSNI derives the Committing Peer's TLS server-name from the
// Lite Peer's (test-network layout: peer0 = LP, peer1 = CP).
func defaultCommitPeerSNI(litePeerSNI string) string {
	if litePeerSNI == "" {
		return ""
	}
	if len(litePeerSNI) >= 5 && litePeerSNI[:5] == "peer0" {
		return "peer1" + litePeerSNI[5:]
	}
	return litePeerSNI
}

// Submit endorses and broadcasts one transaction (writes) or evaluates it
// (reads). It returns as soon as the orderer has accepted the broadcast (T2).
func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	if a.contract == nil {
		return nil, fmt.Errorf("%s: %w", a.name, adapters.ErrNotSetUp)
	}
	fn, args, isRead := a.mapTx(tx)
	t1 := time.Now()

	proposal, err := a.contract.NewProposal(fn, client.WithBytesArguments(args...))
	if err != nil {
		return nil, fmt.Errorf("new proposal %s: %w", fn, err)
	}

	if isRead {
		if _, err := proposal.EvaluateWithContext(ctx); err != nil {
			return &adapters.SubmitResult{TxID: proposal.TransactionID(), SubmitTime: t1}, fmt.Errorf("evaluate %s: %w", fn, stripGatewayPrefix(err))
		}
		id := proposal.TransactionID()
		a.stash(id, &inflight{readTx: true, created: time.Now()})
		return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
	}

	endorsed, err := proposal.EndorseWithContext(ctx)
	if err != nil {
		return &adapters.SubmitResult{TxID: proposal.TransactionID(), SubmitTime: t1}, fmt.Errorf("endorse %s: %w", fn, stripGatewayPrefix(err))
	}
	commit, err := endorsed.SubmitWithContext(ctx)
	if err != nil {
		// The orderer may have accepted it before the error (e.g. a timeout on the
		// response); such a tx can still commit and is then counted as a failure.
		return &adapters.SubmitResult{TxID: endorsed.TransactionID(), SubmitTime: t1}, fmt.Errorf("submit to orderer: %w", stripGatewayPrefix(err))
	}
	ack := time.Now()
	id := endorsed.TransactionID()
	a.stash(id, &inflight{commit: commit, created: ack})
	return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: ack}, nil
}

// WaitForFinality blocks on the commit status stream for the given tx.
func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	f, ok := a.pending[txID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%s: no in-flight tx %s (submit failed or already waited on)", a.name, txID)
	}
	defer a.unstash(txID)

	if f.readTx {
		return &adapters.FinalityResult{TxID: txID, FinalityTime: time.Now(), Valid: true}, nil
	}

	// Resolve from the block event stream: T3 is when the block was observed,
	// not when this call happened to run.
	if a.cp != nil {
		r, err := a.cp.wait(ctx, txID, timeout)
		if err != nil {
			return nil, err
		}
		return &adapters.FinalityResult{
			TxID: txID, FinalityTime: r.at, BlockNum: r.blockNum, Valid: r.valid, InvalidReason: r.code,
		}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status, err := f.commit.StatusWithContext(cctx)
	now := time.Now()
	if err != nil {
		return nil, fmt.Errorf("commit status: %w", err)
	}
	return &adapters.FinalityResult{
		TxID:         txID,
		FinalityTime: now,
		BlockNum:     status.BlockNumber,
		Valid:        status.Successful,
	}, nil
}

// Query evaluates the Get function.
func (a *Adapter) Query(ctx context.Context, key string) (*adapters.QueryResult, error) {
	if a.contract == nil {
		return nil, fmt.Errorf("%s: %w", a.name, adapters.ErrNotSetUp)
	}
	out, err := a.contract.EvaluateWithContext(ctx, a.cfg.FnGet, client.WithArguments(key))
	if err != nil {
		return &adapters.QueryResult{Key: key}, fmt.Errorf("%s: query %s: %w", a.name, key, err)
	}
	return &adapters.QueryResult{Key: key, Value: out, Found: len(out) > 0}, nil
}

// mapTx turns a normalized Transaction into a chaincode function + args.
func (a *Adapter) mapTx(tx *adapters.Transaction) (fn string, args [][]byte, isRead bool) {
	switch tx.Kind {
	case adapters.TxRead:
		return a.cfg.FnGet, [][]byte{[]byte(tx.Key)}, true
	case adapters.TxTransfer:
		return a.cfg.FnTransfer, [][]byte{
			[]byte(tx.Key), []byte(tx.DestKey), []byte(strconv.FormatInt(tx.Amount, 10)),
		}, false
	default: // TxWrite
		return a.cfg.FnPut, [][]byte{[]byte(tx.Key), tx.Value}, false
	}
}

func (a *Adapter) stash(id string, f *inflight) {
	a.mu.Lock()
	a.pending[id] = f
	a.mu.Unlock()
}

func (a *Adapter) unstash(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

// ---- connection helpers ----

// stripGatewayPrefix drops the SDK's own "endorse error: " style prefix so the
// grouped message does not read "endorse Put: endorse error: ...".
func stripGatewayPrefix(err error) error {
	msg := err.Error()
	for _, p := range []string{"endorse error: ", "submit error: ", "evaluate error: "} {
		if strings.HasPrefix(msg, p) {
			return &prefixStripped{msg: strings.TrimPrefix(msg, p), err: err}
		}
	}
	return err
}

type prefixStripped struct {
	msg string
	err error
}

func (p *prefixStripped) Error() string { return p.msg }
func (p *prefixStripped) Unwrap() error { return p.err }

func dial(endpoint, tlsCACertPath, serverNameOverride string) (*grpc.ClientConn, error) {
	caPEM, err := os.ReadFile(tlsCACertPath)
	if err != nil {
		return nil, fmt.Errorf("read tls ca %s: %w", tlsCACertPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tls ca %s: no PEM certificates", tlsCACertPath)
	}
	creds := credentials.NewClientTLSFromCert(pool, serverNameOverride)
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc client for %s: %w", endpoint, err)
	}
	return conn, nil
}

func loadIdentity(mspID, certPath string) (*identity.X509Identity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read cert %s: %w", certPath, err)
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("cert %s is not a PEM X.509 certificate: %w", certPath, err)
	}
	return identity.NewX509Identity(mspID, cert)
}

func loadSign(keyPath string) (identity.Sign, error) {
	p := keyPath
	if fi, err := os.Stat(keyPath); err == nil && fi.IsDir() {
		// MSP keystore directory: use the first file.
		entries, err := os.ReadDir(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read keystore dir %s: %w", keyPath, err)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("keystore dir %s is empty", keyPath)
		}
		p = filepath.Join(keyPath, entries[0].Name())
	}
	keyPEM, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", p, err)
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("key %s is not a PEM private key: %w", p, err)
	}
	return identity.NewPrivateKeySign(key)
}

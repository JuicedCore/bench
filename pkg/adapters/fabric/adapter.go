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
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

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

	cp *cpListener // block-event finality source; set by Setup for every platform

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

// Setup connects the Gateway SDK to the endorsing peer.
func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	if a.cfg == nil {
		c, err := configFromExtra(a.name, ac.Extra)
		if err != nil {
			return err
		}
		a.cfg = c
	}
	a.pending = map[string]*inflight{}

	id, err := loadIdentity(a.cfg.MSPID, a.cfg.CertPath)
	if err != nil {
		return err
	}
	sign, err := loadSign(a.cfg.KeyPath)
	if err != nil {
		return err
	}
	conn, err := dial(a.cfg.EndorseEndpoint, a.cfg.TLSCACertPath, a.cfg.GatewayPeer)
	if err != nil {
		return err
	}
	a.conn = conn

	gw, err := client.Connect(id,
		client.WithSign(sign),
		client.WithClientConnection(conn),
		client.WithEndorseTimeout(a.cfg.EndorseTimeout),
		client.WithSubmitTimeout(a.cfg.SubmitTimeout),
		client.WithCommitStatusTimeout(a.cfg.CommitStatusTimeout),
		client.WithEvaluateTimeout(a.cfg.EndorseTimeout),
	)
	if err != nil {
		conn.Close()
		return err
	}
	a.gw = gw
	a.contract = gw.GetNetwork(a.cfg.Channel).GetContract(a.cfg.Chaincode)

	// Finality always comes from a block event stream so T3 is stamped at
	// observation (see cpListener). Drunix: the Gateway is on the Lite Peer, which
	// never commits, so watch the separate Committing Peer. Otherwise the gateway
	// peer is the committing peer and its own stream is used.
	if !a.cfg.UseCommitPeerEvents {
		l, lerr := listenBlockEvents(gw, a.cfg.Channel)
		if lerr != nil {
			gw.Close()
			conn.Close()
			return fmt.Errorf("fabric: block-event listener: %w", lerr)
		}
		a.cp = l
	} else {
		cpSNI := a.cfg.CommitPeerGateway
		if cpSNI == "" {
			cpSNI = defaultCommitPeerSNI(a.cfg.GatewayPeer)
		}
		cp, cerr := startCPListener(a.cfg.CommitEndpoint, a.cfg.TLSCACertPath, cpSNI, a.cfg.Channel, id, sign)
		if cerr != nil {
			gw.Close()
			conn.Close()
			return fmt.Errorf("fabric: commit-peer listener: %w", cerr)
		}
		a.cp = cp
	}

	// Fail fast if the chaincode is not reachable.
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := a.contract.EvaluateWithContext(pctx, a.cfg.FnGet, client.WithArguments("__healthcheck__")); err != nil {
		// A "key not found" style error is fine - it proves the chaincode ran.
		// Only a transport/deploy failure should abort. We treat any response as OK
		// and let the first real tx surface hard errors.
		_ = err
	}
	return nil
}

func (a *Adapter) Teardown(context.Context) error {
	a.cp.close()
	if a.gw != nil {
		a.gw.Close()
	}
	if a.conn != nil {
		return a.conn.Close()
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
	fn, args, isRead := a.mapTx(tx)
	t1 := time.Now()

	proposal, err := a.contract.NewProposal(fn, client.WithBytesArguments(args...))
	if err != nil {
		return nil, fmt.Errorf("new proposal: %w", err)
	}

	if isRead {
		if _, err := proposal.EvaluateWithContext(ctx); err != nil {
			return &adapters.SubmitResult{TxID: proposal.TransactionID(), SubmitTime: t1}, fmt.Errorf("evaluate: %w", err)
		}
		id := proposal.TransactionID()
		a.stash(id, &inflight{readTx: true, created: time.Now()})
		return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
	}

	endorsed, err := proposal.EndorseWithContext(ctx)
	if err != nil {
		return &adapters.SubmitResult{TxID: proposal.TransactionID(), SubmitTime: t1}, fmt.Errorf("endorse: %w", err)
	}
	commit, err := endorsed.SubmitWithContext(ctx)
	if err != nil {
		return &adapters.SubmitResult{TxID: endorsed.TransactionID(), SubmitTime: t1}, fmt.Errorf("submit: %w", err)
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
		return nil, fmt.Errorf("fabric: no in-flight tx %s", txID)
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
			TxID: txID, FinalityTime: r.at, BlockNum: r.blockNum, Valid: r.valid,
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
	out, err := a.contract.EvaluateWithContext(ctx, a.cfg.FnGet, client.WithArguments(key))
	if err != nil {
		return &adapters.QueryResult{Key: key}, nil
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

func dial(endpoint, tlsCACertPath, serverNameOverride string) (*grpc.ClientConn, error) {
	caPEM, err := os.ReadFile(tlsCACertPath)
	if err != nil {
		return nil, fmt.Errorf("read tls ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("tls ca %s: no PEM certificates", tlsCACertPath)
	}
	creds := credentials.NewClientTLSFromCert(pool, serverNameOverride)
	return grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))
}

func loadIdentity(mspID, certPath string) (*identity.X509Identity, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		return nil, err
	}
	return identity.NewX509Identity(mspID, cert)
}

func loadSign(keyPath string) (identity.Sign, error) {
	p := keyPath
	if fi, err := os.Stat(keyPath); err == nil && fi.IsDir() {
		// MSP keystore directory: use the first file.
		entries, err := os.ReadDir(keyPath)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("keystore dir %s is empty", keyPath)
		}
		p = filepath.Join(keyPath, entries[0].Name())
	}
	keyPEM, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, err
	}
	return identity.NewPrivateKeySign(key)
}

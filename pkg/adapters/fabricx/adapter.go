// Package fabricx is the PlatformAdapter for Fabric-X (Arma ordering + FSC views
// + Token SDK). PHASE 3 - functional against the assumed REST contract in
// fsc_client.go; the routes and payloads are overridable and must be verified
// against the real Fabric-X tokens sample before quoting numbers.
//
// Design (docs/platforms/fabric-x.md, adr-003):
//   - normalized kv-*        -> a custom minimal FSC "kv-write" view (KVRoute)
//   - native token-transfer  -> Token SDK Transfer over the REST API (TransferRoute)
//   - Submit returns when the REST façade accepts the request (T2)
//   - WaitForFinality polls / long-polls the commit status (T3)
//   - the FSC client node + REST server sit in the measured path and are
//     disclosed in the manifest, not "corrected"
package fabricx

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "fabricx"

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter implements adapters.PlatformAdapter for Fabric-X via its REST façade.
type Adapter struct {
	cfg *Config
	cl  *fscClient

	mu   sync.Mutex
	kind map[string]adapters.TxKind // txID -> kind, so reads finalize immediately
}

func (a *Adapter) Name() string { return platformName }

func (a *Adapter) PlatformVersion() string { return platformName }

// CryptoInfo: Fabric-X keeps X.509 MSP identity and verifies signatures in the
// committer, like the EOV platforms.
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote:                "Fabric-X: FSC view/session + Token SDK; committer verifies signatures. REST/FSC node is in the measured path.",
	}
}

func (a *Adapter) MetricsEndpoint() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.MetricsEndpointURL
}

func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	cfg, err := configFromExtra(ac.Extra)
	if err != nil {
		return err
	}
	a.cfg = cfg
	a.cl = newFSCClient(cfg)
	a.kind = map[string]adapters.TxKind{}

	// Reachability check: a status GET for a bogus id should return *something*
	// (404 / not-found JSON), not a connection error.
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := a.cl.status(pctx, "__healthcheck__"); err != nil {
		// tolerate HTTP errors (route may 404), fail only on transport errors
		if isTransportErr(err) {
			return fmt.Errorf("fabricx: REST façade unreachable at %s: %w", cfg.BaseURL, err)
		}
	}
	return nil
}

func (a *Adapter) Teardown(context.Context) error { return nil }

func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	t1 := time.Now()
	var (
		resp *submitResp
		err  error
	)
	switch tx.Kind {
	case adapters.TxRead:
		resp, err = a.cl.kvRead(ctx, tx.Key)
	case adapters.TxTransfer:
		resp, err = a.cl.transfer(ctx, tx.Key, tx.DestKey, tx.Amount)
	default: // TxWrite
		resp, err = a.cl.kvWrite(ctx, tx.Key, tx.Value)
	}
	if err != nil {
		id := ""
		if resp != nil {
			id = resp.TxID
		}
		return &adapters.SubmitResult{TxID: id, SubmitTime: t1}, err
	}
	a.mu.Lock()
	a.kind[resp.TxID] = tx.Kind
	a.mu.Unlock()
	return &adapters.SubmitResult{TxID: resp.TxID, SubmitTime: t1, AckTime: time.Now()}, nil
}

func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	k, known := a.kind[txID]
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.kind, txID)
		a.mu.Unlock()
	}()

	if known && k == adapters.TxRead {
		return &adapters.FinalityResult{TxID: txID, FinalityTime: time.Now(), Valid: true}, nil
	}

	s, err := a.cl.waitFinality(ctx, txID, timeout)
	now := time.Now()
	if err != nil {
		return nil, err
	}
	return &adapters.FinalityResult{
		TxID:         txID,
		FinalityTime: now,
		BlockNum:     s.BlockNum,
		Valid:        s.Status == "committed",
	}, nil
}

func (a *Adapter) Query(ctx context.Context, key string) (*adapters.QueryResult, error) {
	resp, err := a.cl.kvRead(ctx, key)
	if err != nil {
		return &adapters.QueryResult{Key: key}, nil
	}
	val, _ := base64.StdEncoding.DecodeString(resp.Value)
	return &adapters.QueryResult{Key: key, Value: val, Found: resp.Found}, nil
}

func isTransportErr(err error) bool {
	// crude: HTTP-status errors from this package start with "fabricx ... HTTP".
	// anything else (dial/DNS/timeout) is transport.
	s := err.Error()
	for i := 0; i+4 < len(s); i++ {
		if s[i:i+4] == "HTTP" {
			return false
		}
	}
	return true
}

// Package fabricx is the PlatformAdapter for Fabric-X (Arma ordering + FSC views
// + Token SDK). It is an HTTP client for the fabric-x-samples "tokens" REST
// services plus one custom "kv-write" view service
// (deploy/docker/fabricx/kvview/) that provides the normalized KV path Fabric-X
// otherwise lacks.
//
// Verified against hyperledger/fabric-x-samples/tokens/swagger.yaml:
//   - token transfer POST (/owner/accounts/{id}/transfer) and issue POST
//     (/issuer/issue) are SYNCHRONOUS to finality - the sample runs
//     ttx.NewOrderingAndFinalityView before responding. There is therefore no
//     separable submit-ack (T2) for Fabric-X: Submit kicks the POST off in the
//     background and reports AckTime=now (advisory); WaitForFinality returns
//     when the POST completes (T3). Submit latency is reported N/A in the
//     manifest / fairness table. See docs/architecture/fairness-guarantees.md
//     and docs/decisions/adr-003-fabricx-fsc-view-and-rest.md.
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

// Adapter implements adapters.PlatformAdapter for Fabric-X via its REST services.
type Adapter struct {
	cfg *Config
	cl  *fscClient

	mu      sync.Mutex
	pending map[string]chan finalityMsg
}

type finalityMsg struct {
	realTxID string
	valid    bool
	err      error
	at       time.Time
}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) PlatformVersion() string { return platformName }

func (a *Adapter) MetricsEndpoint() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.MetricsEndpointURL
}

// CryptoInfo: Fabric-X keeps X.509 MSP identity; the committer verifies
// signatures. The REST/FSC node sits in the measured path (disclosed, not
// corrected).
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote:                "Fabric-X: FSC view/session + Token SDK; committer verifies signatures. REST/FSC node is in the measured path; submit latency (T2-T1) is N/A - the sample POST blocks to finality.",
	}
}

func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	cfg, err := configFromExtra(ac.Extra)
	if err != nil {
		return err
	}
	a.cfg = cfg
	a.cl = newFSCClient(cfg)
	a.pending = map[string]chan finalityMsg{}

	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.cl.health(pctx); err != nil {
		return fmt.Errorf("fabricx: REST services unreachable (%s / %s): %w", cfg.OwnerURL, cfg.KVURL, err)
	}
	return nil
}

// Teardown drains the pending map. The REST calls themselves are detached
// goroutines bounded by HTTPTimeout, so they cannot be cancelled here, but
// closing out the map stops WaitForFinality callers blocking forever on a tx
// whose goroutine outlived the run, and releases the channels.
func (a *Adapter) Teardown(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.pending {
		delete(a.pending, id)
	}
	return nil
}

// Submit starts the (synchronous-to-finality) REST call in the background and
// returns immediately. The returned TxID is a local correlation id; the real
// platform tx id is delivered to WaitForFinality.
func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	t1 := time.Now()
	localID := fmt.Sprintf("fx-%d-%d", tx.Seq, t1.UnixNano())

	ch := make(chan finalityMsg, 1)
	a.mu.Lock()
	a.pending[localID] = ch
	a.mu.Unlock()

	go func() {
		// Detached from ctx: the load generator's per-submit ctx is short-lived,
		// but this call legitimately runs until finality.
		cctx, cancel := context.WithTimeout(context.Background(), a.cfg.HTTPTimeout)
		defer cancel()

		var (
			realID string
			err    error
		)
		switch tx.Kind {
		case adapters.TxRead:
			var kr *kvResponse
			kr, err = a.cl.kvRead(cctx, tx.Key)
			if kr != nil {
				realID = kr.TxID
			}
		case adapters.TxTransfer:
			realID, err = a.cl.transfer(cctx, orDefault(tx.Key, a.cfg.SenderAccount), tx.DestKey, uint64(max64(tx.Amount, 1)))
		default: // TxWrite
			if a.cfg.KVURL != "" {
				var kr *kvResponse
				kr, err = a.cl.kvWrite(cctx, tx.Key, tx.Value)
				if kr != nil {
					realID = kr.TxID
				}
			} else {
				realID, err = a.cl.issue(cctx, orDefault(tx.Key, a.cfg.SenderAccount), 1)
			}
		}
		ch <- finalityMsg{realTxID: realID, valid: err == nil, err: err, at: time.Now()}
	}()

	return &adapters.SubmitResult{TxID: localID, SubmitTime: t1, AckTime: time.Now()}, nil
}

// WaitForFinality blocks until the background REST call for localID completes.
func (a *Adapter) WaitForFinality(ctx context.Context, localID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	ch, ok := a.pending[localID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fabricx: no in-flight tx %s", localID)
	}
	defer func() {
		a.mu.Lock()
		delete(a.pending, localID)
		a.mu.Unlock()
	}()

	select {
	case m := <-ch:
		id := m.realTxID
		if id == "" {
			id = localID
		}
		// A refusal by the REST facade is a terminal platform outcome, so report
		// it as an invalid tx rather than an adapter error - otherwise every
		// rejection lands in the "errored" bucket and the failure-rate breakdown
		// cannot distinguish "the platform said no" from "we could not reach it".
		// Transport failures still surface as errors.
		if m.err != nil {
			if isRejected(m.err) {
				return &adapters.FinalityResult{TxID: id, FinalityTime: m.at, Valid: false}, nil
			}
			return nil, m.err
		}
		return &adapters.FinalityResult{TxID: id, FinalityTime: m.at, Valid: m.valid}, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("fabricx: finality timeout for %s", localID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Query reads an account balance (token workloads) or a kv key (normalized).
func (a *Adapter) Query(ctx context.Context, key string) (*adapters.QueryResult, error) {
	if a.cfg.KVURL != "" {
		kr, err := a.cl.kvRead(ctx, key)
		if err != nil || kr == nil {
			return &adapters.QueryResult{Key: key}, nil
		}
		val, _ := base64.StdEncoding.DecodeString(kr.Value)
		return &adapters.QueryResult{Key: key, Value: val, Found: kr.Found}, nil
	}
	acct, err := a.cl.balance(ctx, orDefault(key, a.cfg.SenderAccount))
	if err != nil || acct == nil {
		return &adapters.QueryResult{Key: key}, nil
	}
	for _, b := range acct.Balance {
		if b.Code == a.cfg.TokenCode {
			return &adapters.QueryResult{Key: key, Value: []byte(fmt.Sprintf("%d", b.Value)), Found: true}, nil
		}
	}
	return &adapters.QueryResult{Key: key, Found: true}, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

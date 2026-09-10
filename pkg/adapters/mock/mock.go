// Package mock is an in-process PlatformAdapter used for harness self-tests and
// smoke-testing the load generator without a real network. It simulates submit
// and commit latency and an optional MVCC-style conflict rate on write-path
// transactions.
package mock

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

func init() { adapters.Register("mock", func() adapters.PlatformAdapter { return &Adapter{} }) }

// Adapter is the mock.
type Adapter struct {
	// rngMu guards rng only.
	rngMu sync.Mutex
	rng   *rand.Rand

	// mu guards state, pending and blockNum.
	mu      sync.Mutex
	state   map[string][]byte
	pending map[string]*pendingTx
	blockNum uint64

	submitBase time.Duration
	commitBase time.Duration
	jitter     time.Duration
	conflict   float64
}

type pendingTx struct {
	done   chan struct{}
	result *adapters.FinalityResult
}

// Setup reads optional knobs from cfg.Extra: submit_ms, commit_ms, jitter_ms,
// conflict_rate.
func (a *Adapter) Setup(_ context.Context, cfg adapters.AdapterConfig) error {
	a.rng = rand.New(rand.NewSource(1))
	a.state = map[string][]byte{}
	a.pending = map[string]*pendingTx{}
	a.submitBase = dur(cfg.Extra, "submit_ms", 2)
	a.commitBase = dur(cfg.Extra, "commit_ms", 40)
	a.jitter = dur(cfg.Extra, "jitter_ms", 15)
	if v, ok := cfg.Extra["conflict_rate"].(float64); ok {
		a.conflict = v
	}
	return nil
}

func (a *Adapter) Name() string            { return "mock" }
func (a *Adapter) MetricsEndpoint() string { return "" }
func (a *Adapter) PlatformVersion() string { return "mock-0" }

func (a *Adapter) Teardown(context.Context) error { return nil }

// Submit simulates SDK + endorsement latency then schedules a commit.
func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	t1 := time.Now()
	submitDelay := a.submitBase + a.randJitter()
	commitDelay := a.commitBase + a.randJitter()
	writePath := tx.Kind == adapters.TxWrite || tx.Kind == adapters.TxTransfer
	invalid := writePath && a.randFloat() < a.conflict

	sleep(ctx, submitDelay)
	id := fmt.Sprintf("mock-%d-%d", tx.Seq, time.Now().UnixNano())

	pt := &pendingTx{done: make(chan struct{})}
	a.mu.Lock()
	a.pending[id] = pt
	a.mu.Unlock()

	go func() {
		time.Sleep(commitDelay)
		a.mu.Lock()
		a.blockNum++
		bn := a.blockNum
		if !invalid && tx.Kind == adapters.TxWrite {
			a.state[tx.Key] = append([]byte(nil), tx.Value...)
		}
		a.mu.Unlock()
		pt.result = &adapters.FinalityResult{
			TxID: id, FinalityTime: time.Now(), BlockNum: bn, Valid: !invalid,
		}
		close(pt.done)
	}()

	return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
}

// WaitForFinality blocks on the simulated commit.
func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	pt, ok := a.pending[txID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown tx %s", txID)
	}
	select {
	case <-pt.done:
		a.mu.Lock()
		delete(a.pending, txID)
		a.mu.Unlock()
		return pt.result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("finality timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Query reads simulated world state.
func (a *Adapter) Query(_ context.Context, key string) (*adapters.QueryResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.state[key]
	return &adapters.QueryResult{Key: key, Value: v, Found: ok}, nil
}

func (a *Adapter) randJitter() time.Duration {
	if a.jitter <= 0 {
		return 0
	}
	a.rngMu.Lock()
	defer a.rngMu.Unlock()
	return time.Duration(a.rng.Int63n(int64(a.jitter)))
}

func (a *Adapter) randFloat() float64 {
	a.rngMu.Lock()
	defer a.rngMu.Unlock()
	return a.rng.Float64()
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func dur(m map[string]any, key string, defMs float64) time.Duration {
	if m != nil {
		if v, ok := m[key]; ok {
			switch n := v.(type) {
			case float64:
				return time.Duration(n * float64(time.Millisecond))
			case int:
				return time.Duration(n) * time.Millisecond
			}
		}
	}
	return time.Duration(defMs * float64(time.Millisecond))
}

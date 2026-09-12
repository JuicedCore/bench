package loadgen_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
	"github.com/juicedcore/bench/pkg/metrics"
)

// fakeAdapter records submit count and returns instantly; finality resolves
// after a small fixed delay.
type fakeAdapter struct {
	submits atomic.Int64
	commit  time.Duration
}

func (f *fakeAdapter) Name() string                                  { return "fake" }
func (f *fakeAdapter) Setup(context.Context, adapters.AdapterConfig) error { return nil }
func (f *fakeAdapter) Teardown(context.Context) error                { return nil }
func (f *fakeAdapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return &adapters.QueryResult{}, nil
}
func (f *fakeAdapter) MetricsEndpoint() string { return "" }

func (f *fakeAdapter) Submit(_ context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	n := f.submits.Add(1)
	return &adapters.SubmitResult{
		TxID:       "fake-" + itoa(n),
		SubmitTime: time.Now(),
		AckTime:    time.Now(),
	}, nil
}

func (f *fakeAdapter) WaitForFinality(_ context.Context, id string, _ time.Duration) (*adapters.FinalityResult, error) {
	time.Sleep(f.commit)
	return &adapters.FinalityResult{TxID: id, FinalityTime: time.Now(), Valid: true}, nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type txSrc struct{ seq atomic.Uint64 }

func (s *txSrc) Next(seq uint64) *adapters.Transaction {
	return &adapters.Transaction{Kind: adapters.TxWrite, Key: "k", Value: []byte("v"), Seq: seq}
}

func TestOpenLoopHitsOfferedRate(t *testing.T) {
	fa := &fakeAdapter{commit: 5 * time.Millisecond}
	col := metrics.NewCollector()
	g := &loadgen.Generator{Adapter: fa, Source: &txSrc{}, Collector: col}

	const target = 500
	const dur = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := g.Run(ctx, loadgen.LoadProfile{
		Mode: loadgen.OpenLoop, TargetTPS: target, Duration: dur, FinalityWait: 2 * time.Second,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)

	got := fa.submits.Load()
	want := float64(target) * dur.Seconds()
	// within 15% - scheduling jitter + drain
	if float64(got) < want*0.85 || float64(got) > want*1.20 {
		t.Errorf("submitted %d, want ~%.0f (target %d TPS x %s)", got, want, target, dur)
	}
	if elapsed > dur+3*time.Second {
		t.Errorf("run took %s, expected close to %s", elapsed, dur)
	}

	res := col.Aggregate(metrics.Window{Start: start, End: start.Add(dur)})
	if !res.InvariantOK {
		t.Errorf("invariant broken: submitted=%d committed=%d errored=%d timedout=%d",
			res.Submitted, res.Committed, res.Errored, res.TimedOut)
	}
	if res.Committed == 0 {
		t.Error("no committed transactions recorded")
	}
}

func TestClosedLoopBoundedByWorkers(t *testing.T) {
	fa := &fakeAdapter{commit: 20 * time.Millisecond}
	col := metrics.NewCollector()
	g := &loadgen.Generator{Adapter: fa, Source: &txSrc{}, Collector: col}

	const workers = 10
	const dur = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := g.Run(ctx, loadgen.LoadProfile{
		Mode: loadgen.ClosedLoop, Workers: workers, Duration: dur, FinalityWait: 2 * time.Second,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := fa.submits.Load()
	// each worker: submit + ~21ms wait => ~<= dur/21ms iterations. Upper bound
	// with slack: workers * dur/commit * 1.5
	upper := float64(workers) * dur.Seconds() / 0.020 * 1.5
	if float64(got) > upper {
		t.Errorf("closed-loop submitted %d, exceeds worker-bounded upper %.0f", got, upper)
	}
	if got < int64(workers) {
		t.Errorf("closed-loop submitted only %d, expected at least one round per worker", got)
	}
}

// Slow finality observation must not throttle submission. An adapter whose
// WaitForFinality takes 200ms, driven at 1000 TPS, needs ~200 transactions
// outstanding at once - comfortably inside a 500-slot in-flight cap.
//
// The old design fed a fixed pool of 64 finality workers (~320 waits/s here)
// through a buffered channel. Once that fell behind, submit goroutines blocked
// holding their in-flight slot, the cap filled, and the schedule stalled: the
// generator throttled itself and the platform was blamed for the send gap and the
// queueing latency. The cap is pinned here so that happens within a short test;
// with the old 4-seconds-of-load default it takes ~6s, well inside a real 60s
// sweep step. The historical fabric-cft sweep recorded a 246s send-gap p99.
func TestOpenLoopSlowFinalityDoesNotThrottleSubmission(t *testing.T) {
	fa := &fakeAdapter{commit: 200 * time.Millisecond}
	col := metrics.NewCollector()
	g := &loadgen.Generator{Adapter: fa, Source: &txSrc{}, Collector: col}

	const target = 1000
	const dur = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	if err := g.Run(ctx, loadgen.LoadProfile{
		Mode: loadgen.OpenLoop, TargetTPS: target, Duration: dur, FinalityWait: 5 * time.Second,
		MaxInFlight: 500,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := fa.submits.Load()
	want := float64(target) * dur.Seconds()
	if float64(got) < want*0.85 {
		t.Errorf("submitted %d, want ~%.0f: slow finality throttled the offered rate", got, want)
	}

	res := col.Aggregate(metrics.Window{Start: start, End: start.Add(dur)})
	if p99 := res.SendGap.Percentiles["p99"]; p99 > 50 {
		t.Errorf("send_gap p99 = %.1f ms, want <= 50 (the documented reject threshold): the generator fell behind its own schedule", p99)
	}
	// E2E should be the adapter's 200ms, not 200ms plus time queued for a worker.
	if p50 := res.E2E.Percentiles["p50"]; p50 > 400 {
		t.Errorf("e2e p50 = %.0f ms, want ~200: finality was observed late and queueing billed as latency", p50)
	}
	if !res.InvariantOK {
		t.Errorf("invariant broken: submitted=%d committed=%d timedout=%d", res.Submitted, res.Committed, res.TimedOut)
	}
}

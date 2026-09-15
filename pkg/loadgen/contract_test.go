package loadgen_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
	"github.com/juicedcore/bench/pkg/metrics"
)

// brokenAdapter violates the contract in configurable ways.
type brokenAdapter struct {
	fakeAdapter
	nilResult  bool
	streamDown bool
}

func (b *brokenAdapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	if b.nilResult {
		return nil, nil
	}
	return b.fakeAdapter.Submit(ctx, tx)
}

func (b *brokenAdapter) WaitForFinality(ctx context.Context, id string, d time.Duration) (*adapters.FinalityResult, error) {
	if b.streamDown {
		return nil, fmt.Errorf("x: %w: deliver stream closed", adapters.ErrFinalityStreamDown)
	}
	return b.fakeAdapter.WaitForFinality(ctx, id, d)
}

func runBroken(t *testing.T, a adapters.PlatformAdapter) metrics.Result {
	t.Helper()
	col := metrics.NewCollector()
	g := &loadgen.Generator{Adapter: a, Source: &txSrc{}, Collector: col}
	start := time.Now()
	if err := g.Run(context.Background(), loadgen.LoadProfile{Mode: loadgen.OpenLoop, TargetTPS: 100, Duration: 300 * time.Millisecond, FinalityWait: time.Second}); err != nil {
		t.Fatal(err)
	}
	return col.Aggregate(metrics.Window{Start: start.Add(-time.Second), End: time.Now().Add(time.Second)})
}

func TestNilSubmitResultIsAFailureNotAPanic(t *testing.T) {
	r := runBroken(t, &brokenAdapter{nilResult: true})
	if r.Submitted < 20 || r.Errored != r.Submitted {
		t.Fatalf("submitted=%d errored=%d: each nil result must be its own failure", r.Submitted, r.Errored)
	}
	if !strings.Contains(r.Errors[0].Message, "nil result") {
		t.Errorf("error should name the adapter bug: %+v", r.Errors)
	}
}

func TestStreamDownCountsAsErrorNotTimeout(t *testing.T) {
	r := runBroken(t, &brokenAdapter{streamDown: true})
	if r.Errored == 0 || r.TimedOut != 0 {
		t.Fatalf("errored=%d timed_out=%d: a dead finality stream is an error", r.Errored, r.TimedOut)
	}
}

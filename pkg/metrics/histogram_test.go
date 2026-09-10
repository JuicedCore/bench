package metrics

import (
	"testing"
	"time"
)

func TestLatencyPercentileKeys(t *testing.T) {
	l := NewLatency("t")
	for i := 0; i < 1000; i++ {
		l.Record(10 * time.Millisecond)
	}
	s := l.Snapshot()
	for _, k := range []string{"p1", "p50", "p95", "p99", "p99.9", "p99.99"} {
		if _, ok := s.Percentiles[k]; !ok {
			t.Errorf("missing percentile key %q in %v", k, s.Percentiles)
		}
	}
	if s.Count != 1000 {
		t.Errorf("count = %d, want 1000", s.Count)
	}
	if s.Percentiles["p50"] < 9 || s.Percentiles["p50"] > 11 {
		t.Errorf("p50 = %.2f ms, want ~10", s.Percentiles["p50"])
	}
}

func TestLatencyMerge(t *testing.T) {
	a := NewLatency("a")
	b := NewLatency("b")
	for i := 0; i < 500; i++ {
		a.Record(5 * time.Millisecond)
		b.Record(50 * time.Millisecond)
	}
	a.Merge(b)
	s := a.Snapshot()
	if s.Count != 1000 {
		t.Fatalf("merged count = %d, want 1000", s.Count)
	}
	if s.Percentiles["p1"] > 10 || s.Percentiles["p99"] < 40 {
		t.Errorf("merged distribution wrong: p1=%.2f p99=%.2f", s.Percentiles["p1"], s.Percentiles["p99"])
	}
}

func TestLatencyClampHigh(t *testing.T) {
	l := NewLatency("t")
	l.Record(10 * time.Hour) // beyond 5min range - must not panic, clamps
	s := l.Snapshot()
	if s.Count != 1 {
		t.Fatalf("count = %d, want 1", s.Count)
	}
	if s.MaxMs <= 0 {
		t.Errorf("clamped max should be positive, got %.2f", s.MaxMs)
	}
}

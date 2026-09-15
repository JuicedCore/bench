// Package metrics is the measurement pipeline: per-transaction T1/T2/T3 timing,
// HDR-histogram latency aggregation, platform-native Prometheus scraping, and
// host resource sampling. See docs/architecture/metrics-methodology.md.
package metrics

import (
	"math"
	"strconv"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// reported is the fixed percentile spectrum emitted for every latency metric.
// Kept as a package var so the reporter and tests agree.
var reported = []float64{1, 5, 10, 25, 50, 75, 90, 95, 99, 99.9, 99.99}

// maxLatency is the top of every latency histogram's range; larger values are
// clamped to it (see Collector.ClampedLatencies).
const maxLatency = 5 * time.Minute

// Latency wraps an HDR histogram with a mutex so many load-generator goroutines
// can record concurrently. Values are stored in microseconds.
type Latency struct {
	mu   sync.Mutex
	h    *hdr.Histogram
	name string
}

// NewLatency returns a Latency recorder covering 1us .. 5min with 3 significant
// figures, which is ample for blockchain commit latencies.
func NewLatency(name string) *Latency {
	return &Latency{
		h:    hdr.New(1, int64(maxLatency/time.Microsecond), 3),
		name: name,
	}
}

// Record adds one observation.
func (l *Latency) Record(d time.Duration) {
	us := d.Microseconds()
	if us < 1 {
		us = 1
	}
	l.mu.Lock()
	// RecordValue only errors when the value is out of range; clamp to max.
	if err := l.h.RecordValue(us); err != nil {
		_ = l.h.RecordValue(l.h.HighestTrackableValue())
	}
	l.mu.Unlock()
}

// Snapshot is an immutable view of a Latency at a point in time.
type Snapshot struct {
	Name        string             `json:"name"`
	Count       int64              `json:"count"`
	MinMs       float64            `json:"min_ms"`
	MaxMs       float64            `json:"max_ms"`
	MeanMs      float64            `json:"mean_ms"`
	StdDevMs    float64            `json:"stddev_ms"`
	Percentiles map[string]float64 `json:"percentiles_ms"`
}

// Snapshot computes summary statistics. Safe to call while recording continues.
func (l *Latency) Snapshot() Snapshot {
	l.mu.Lock()
	c := l.h.Export()
	l.mu.Unlock()
	h := hdr.Import(c)

	s := Snapshot{
		Name:        l.name,
		Count:       h.TotalCount(),
		MinMs:       usToMs(h.Min()),
		MaxMs:       usToMs(h.Max()),
		MeanMs:      h.Mean() / 1000.0,
		StdDevMs:    h.StdDev() / 1000.0,
		Percentiles: map[string]float64{},
	}
	for _, p := range reported {
		s.Percentiles[pctKey(p)] = usToMs(h.ValueAtQuantile(p))
	}
	return s
}

// Merge folds another Latency's data into this one. Used to combine per-worker
// histograms without lock contention on the hot path.
func (l *Latency) Merge(other *Latency) {
	other.mu.Lock()
	c := other.h.Export()
	other.mu.Unlock()
	l.mu.Lock()
	l.h.Merge(hdr.Import(c))
	l.mu.Unlock()
}

func usToMs(us int64) float64 {
	if us < 0 {
		return 0
	}
	return math.Round(float64(us)/10.0) / 100.0 // 2 decimal places, in ms
}

func pctKey(p float64) string {
	return "p" + strconv.FormatFloat(p, 'f', -1, 64)
}

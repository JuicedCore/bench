package harness

import (
	"strings"
	"testing"

	"github.com/juicedcore/bench/pkg/metrics"
)

func sweepDefaults() SweepConfig {
	return SweepConfig{MaxFailRate: 0.02, GoodputRatio: 0.95, MaxSendGapMs: 50}
}

func resultWith(confirmed, failRate, sendGapP99 float64) metrics.Result {
	return metrics.Result{
		ConfirmedTPS: confirmed,
		FailureRate:  failRate,
		SendGap:      metrics.Snapshot{Percentiles: map[string]float64{"p99": sendGapP99}},
	}
}

// The numbers from the only real capacity run on record,
// results/fabric-cft/20260911-143752. Failure rate was 0.0000 on every step, so
// the old failure-rate-only rule "held" all the way to 10000 TPS - the top of the
// ladder - and headlined a figure ten times the real knee. Confirmed throughput
// shows the knee plainly: 1000.
func TestSaturationFromTheHistoricalFabricRun(t *testing.T) {
	s := sweepDefaults()
	phases := []PhaseResult{
		{Name: "sweep-1000", OfferedTPS: 1000, Result: resultWith(1000, 0, 5)},
		{Name: "sweep-2000", OfferedTPS: 2000, Result: resultWith(1753, 0, 5)},
		{Name: "sweep-5000", OfferedTPS: 5000, Result: resultWith(618, 0, 5)},
		{Name: "sweep-10000", OfferedTPS: 10000, Result: resultWith(0, 0, 5)},
	}
	if got := detectSaturation(phases, s); got != 1000 {
		t.Errorf("saturation = %d, want 1000", got)
	}
}

func TestStepVerdictRules(t *testing.T) {
	s := sweepDefaults()
	cases := []struct {
		name    string
		offered int
		r       metrics.Result
		wantSub string // "" = held
	}{
		{"healthy", 1000, resultWith(990, 0.001, 3), ""},
		{"exactly at goodput floor", 1000, resultWith(950, 0, 3), ""},
		{"too many failures", 1000, resultWith(1000, 0.05, 3), "failure rate"},
		// The case the old rule missed: nothing failed, it just did not get done.
		{"goodput collapse with zero failures", 2000, resultWith(1753, 0, 3), "goodput"},
		{"nothing sent at all", 10000, resultWith(0, 0, 0), "goodput"},
		// Throughput looked fine but the generator was behind schedule, so the
		// step measured the generator rather than the platform.
		{"generator fell behind", 1000, resultWith(1000, 0, 400), "send-gap"},
	}
	for _, c := range cases {
		got := stepVerdict(c.offered, c.r, s)
		switch {
		case c.wantSub == "" && got != "":
			t.Errorf("%s: rejected (%s), want held", c.name, got)
		case c.wantSub != "" && !strings.Contains(got, c.wantSub):
			t.Errorf("%s: verdict %q, want it to mention %q", c.name, got, c.wantSub)
		}
	}
}

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
		// Non-zero so the empty-window rule does not pre-empt the rule under test.
		Submitted:    1,
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
		// An empty window passes every rate rule vacuously (0% failures). Drunix
		// sweep steps recorded exactly this while its generator was stalled.
		{"empty measurement window", 100, metrics.Result{}, "no transactions"},
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

// A run whose deploy did not report the budget it applied must not look like a
// resource-controlled run.
func TestResourceBudgetUnreportedIsCaveated(t *testing.T) {
	for _, k := range []string{"BENCH_RESOURCE_CONTAINERS", "BENCH_RESOURCE_CPUS_EACH", "BENCH_RESOURCE_MEMORY_EACH", "BENCH_RESOURCE_MEMORY_SPLIT"} {
		t.Setenv(k, "")
	}
	var m Manifest
	applyResourceEnv(&m)
	if m.ResourceContainers != 0 || len(m.Caveats) != 1 || !strings.Contains(m.Caveats[0], "not reported") {
		t.Errorf("unreported budget not caveated: containers=%d caveats=%v", m.ResourceContainers, m.Caveats)
	}
}

// The deploy splits memory by role weight; the manifest must carry each
// container's actual limit and the weights, or the budget is unverifiable.
func TestResourceBudgetRecordsWeightedMemorySplit(t *testing.T) {
	t.Setenv("BENCH_RESOURCE_CONTAINERS", "3")
	t.Setenv("BENCH_RESOURCE_CPUS_EACH", "2.67")
	t.Setenv("BENCH_RESOURCE_MEMORY_EACH", "")
	t.Setenv("BENCH_RESOURCE_MEMORY_SPLIT", "orderer.example.com=1638m,peer0.org1.example.com=3276m,dev-peer0=819m")
	t.Setenv("BENCH_RESOURCE_MEMORY_WEIGHTS", `"peer=4 statedb=4 orderer=2 other=1"`)
	t.Setenv("BENCH_RESOURCE_CPUS_TOTAL", "8")
	t.Setenv("BENCH_RESOURCE_MEMORY_TOTAL_GB", "8")
	var m Manifest
	applyResourceEnv(&m)
	if m.ResourceMemory["peer0.org1.example.com"] != "3276m" || m.ResourceMemory["dev-peer0"] != "819m" || len(m.ResourceMemory) != 3 {
		t.Errorf("memory split not recorded: %v", m.ResourceMemory)
	}
	if m.ResourceMemoryWeights != "peer=4 statedb=4 orderer=2 other=1" {
		t.Errorf("weights = %q", m.ResourceMemoryWeights)
	}
}

func TestResourceBudgetRecordsWhatDeployApplied(t *testing.T) {
	t.Setenv("BENCH_RESOURCE_CONTAINERS", "13")
	t.Setenv("BENCH_RESOURCE_CPUS_EACH", "0.62")
	t.Setenv("BENCH_RESOURCE_MEMORY_EACH", "630m")
	t.Setenv("BENCH_RESOURCE_CPUS_TOTAL", "8")
	t.Setenv("BENCH_RESOURCE_MEMORY_TOTAL_GB", "8")
	var m Manifest
	applyResourceEnv(&m)
	if m.ResourceContainers != 13 || m.ResourceLimit.CPUs != 0.62 || m.ResourceLimit.Memory != "630m" ||
		m.ResourceCPUsTotal != 8 || m.ResourceMemTotalGB != 8 || len(m.Caveats) != 0 {
		t.Errorf("manifest did not record the applied budget: %+v caveats=%v", m, m.Caveats)
	}
}

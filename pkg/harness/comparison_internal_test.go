package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

// goodRun is a run that passes every rejection rule.
func goodRun(platform, name string, normalized bool, confTPS float64) RunResult {
	return RunResult{
		Manifest: Manifest{
			RunName: name, Platform: platform, Workload: "kv-write", Profile: "local-small",
			Normalized: normalized, Generators: 2, ResourceContainers: 4,
			ResourceCPUsTotal: 8, ResourceMemTotalGB: 8, StartedAt: time.Now(),
		},
		Headline: &metrics.Result{
			ConfirmedTPS: confTPS, InvariantOK: true, Submitted: 1000, Committed: 1000,
			E2E:     metrics.Snapshot{Percentiles: map[string]float64{"p50": 100, "p99": 300}},
			SendGap: metrics.Snapshot{Percentiles: map[string]float64{"p99": 2}},
		},
	}
}

func rec(rr RunResult) runRecord { return runRecord{RR: rr, Problems: runProblems(rr)} }

func TestRunProblemsExcludeWhatCannotBeCompared(t *testing.T) {
	if p := runProblems(goodRun("fabric-cft", "quick-smoke", true, 50)); len(p) != 0 {
		t.Fatalf("a clean run was rejected: %v", p)
	}

	cases := map[string]func(*RunResult){
		"before the measurement fixes": func(r *RunResult) { r.Manifest.Generators = 0 },
		"invariant broken":             func(r *RunResult) { r.Headline.InvariantOK = false },
		"fell behind":                  func(r *RunResult) { r.Headline.SendGap.Percentiles["p99"] = 400 },
		"resource budget not reported": func(r *RunResult) { r.Manifest.ResourceContainers = 0 },
		// The 2026-09-13 Fabric sweeps: a peer was OOM-killed at 2000 TPS and the
		// hold phase headlined 0.0 TPS with every transaction failed, which the
		// report then ranked as a comparable result.
		"committed nothing": func(r *RunResult) {
			r.Headline.Committed, r.Headline.ConfirmedTPS, r.Headline.FailureRate = 0, 0, 1
		},
		"platform failed mid-run": func(r *RunResult) {
			r.Manifest.ContainerFailures = []metrics.ContainerFailure{{Name: "peer0.org1.example.com", OOMKilled: true, Exited: true, ExitCode: 137}}
		},
		"floor reading": func(r *RunResult) {
			r.Phases = []PhaseResult{{Name: "sweep-100", OfferedTPS: 100}}
			r.SaturationTPS = 0
		},
		// The historical fabric-cft sweep: every step "held", so saturation was the
		// top of the ladder. That is a lower bound, not a knee.
		"never saturated": func(r *RunResult) {
			r.Phases = []PhaseResult{{Name: "sweep-100", OfferedTPS: 100}, {Name: "sweep-10000", OfferedTPS: 10000}}
			r.SaturationTPS = 10000
		},
	}
	for want, mutate := range cases {
		rr := goodRun("fabric-cft", "probe-sweep", true, 50)
		mutate(&rr)
		p := runProblems(rr)
		if len(p) == 0 || !strings.Contains(strings.Join(p, "|"), want) {
			t.Errorf("%q: problems=%v", want, p)
		}
	}
}

// An early-aborted sweep whose best step was the last one actually offered is a
// real knee, not "never saturated".
func TestAbortedSweepAtItsBestStepIsAKnee(t *testing.T) {
	rr := goodRun("fabricx", "probe-sweep", true, 900)
	rr.Phases = []PhaseResult{{Name: "sweep-500", OfferedTPS: 500}, {Name: "sweep-1000", OfferedTPS: 1000}}
	rr.SaturationTPS = 1000
	rr.Manifest.SkippedSteps = []int{2000, 5000}
	if p := runProblems(rr); len(p) != 0 {
		t.Errorf("aborted sweep wrongly rejected: %v", p)
	}
}

func TestComparisonGroupsAndMedians(t *testing.T) {
	bad := goodRun("fabric-cft", "quick-smoke", true, 9999)
	bad.Headline.InvariantOK = false

	recs := []runRecord{
		rec(goodRun("fabric-cft", "quick-smoke", true, 48)),
		rec(goodRun("fabric-cft", "quick-smoke", true, 52)),
		rec(goodRun("fabric-cft", "quick-smoke", true, 50)),
		rec(bad), // must not drag the median
		rec(goodRun("drunix", "quick-smoke", true, 40)),
		// Same config name but native: must land in its own group.
		rec(goodRun("fabric-cft", "quick-smoke", false, 500)),
		// Different profile: not comparable with the rest.
		func() runRecord {
			r := goodRun("fabric-cft", "quick-smoke", true, 70)
			r.Manifest.Profile = "local"
			return rec(r)
		}(),
	}
	groups := buildComparison(recs)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 (normalized/local-small, normalized/local, native)", len(groups))
	}
	g := groups[0]
	if !g.Normalized || g.Profile != "local" && g.Profile != "local-small" {
		t.Fatalf("normalized groups must sort first, got %+v", g)
	}
	var main *comparisonGroup
	for _, x := range groups {
		if x.Normalized && x.Profile == "local-small" {
			main = x
		}
	}
	if main == nil || len(main.Rows) != 2 {
		t.Fatalf("local-small normalized group should hold fabric-cft and drunix: %+v", main)
	}
	top := main.Rows[0]
	if top.Platform != "fabric-cft" {
		t.Errorf("rows should rank by median TPS, got %s first", top.Platform)
	}
	if top.MedConfTPS != 50 || top.MinConfTPS != 48 || top.MaxConfTPS != 52 {
		t.Errorf("median/spread = %.0f %.0f-%.0f, want 50 48-52 (the invalid 9999 run excluded)",
			top.MedConfTPS, top.MinConfTPS, top.MaxConfTPS)
	}
	if len(top.Valid) != 3 || len(top.Invalid) != 1 {
		t.Errorf("valid/invalid = %d/%d, want 3/1", len(top.Valid), len(top.Invalid))
	}

	html := renderComparison(groups, recs, "results", ReportOptions{})
	ni, pi := strings.Index(html, "Normalized comparison"), strings.Index(html, "Platform-native runs")
	if ni < 0 || pi < 0 || ni > pi {
		t.Error("normalized section must exist and precede the native section")
	}
	if !strings.Contains(html, "invariant broken") {
		t.Error("the excluded run's reason is not shown")
	}
}

func TestReportEscapesCaveats(t *testing.T) {
	rr := goodRun("fabricx", "quick-smoke", true, 10)
	rr.Manifest.Caveats = []string{"<script>alert(1)</script>"}
	out := renderComparison(buildComparison([]runRecord{rec(rr)}), []runRecord{rec(rr)}, "r", ReportOptions{})
	if strings.Contains(out, "<script>alert") {
		t.Error("caveat text was not HTML-escaped")
	}
}

func TestMedianEvenAndOdd(t *testing.T) {
	if m := median([]float64{3, 1, 2}); m != 2 {
		t.Errorf("odd median = %v", m)
	}
	if m := median([]float64{4, 1, 3, 2}); m != 2.5 {
		t.Errorf("even median = %v", m)
	}
}

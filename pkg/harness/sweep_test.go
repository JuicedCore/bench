package harness_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/harness"
)

// kneeAdapter commits cleanly up to a configured offered rate and starts
// returning invalid transactions above it. The stock mock adapter's
// conflict_rate is a flat probability, so it cannot produce a saturation knee -
// and a knee is exactly what the hold-phase retargeting has to find.
type kneeAdapter struct {
	knee float64

	mu       sync.Mutex
	bucket   time.Time
	inBucket int
	pending  map[string]bool
}

const kneeBucket = 200 * time.Millisecond

func (a *kneeAdapter) Name() string            { return "kneemock" }
func (a *kneeAdapter) PlatformVersion() string { return "kneemock-0" }
func (a *kneeAdapter) MetricsEndpoint() string { return "" }

func (a *kneeAdapter) Setup(_ context.Context, cfg adapters.AdapterConfig) error {
	a.knee = 150
	if v, ok := cfg.Extra["knee_tps"].(float64); ok {
		a.knee = v
	}
	a.pending = map[string]bool{}
	a.bucket = time.Now()
	return nil
}

func (a *kneeAdapter) Teardown(context.Context) error { return nil }

// Submit classifies against the offered rate observed over the last bucket, so
// the failure rate steps up sharply once the ladder passes knee_tps.
func (a *kneeAdapter) Submit(_ context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	t1 := time.Now()

	a.mu.Lock()
	if t1.Sub(a.bucket) >= kneeBucket {
		a.bucket = t1
		a.inBucket = 0
	}
	a.inBucket++
	rate := float64(a.inBucket) * float64(time.Second) / float64(kneeBucket)
	id := fmt.Sprintf("knee-%d-%d", tx.Seq, t1.UnixNano())
	a.pending[id] = rate <= a.knee
	a.mu.Unlock()

	return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
}

func (a *kneeAdapter) WaitForFinality(_ context.Context, txID string, _ time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	valid, ok := a.pending[txID]
	delete(a.pending, txID)
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("kneemock: unknown tx %s", txID)
	}
	return &adapters.FinalityResult{TxID: txID, FinalityTime: time.Now(), BlockNum: 1, Valid: valid}, nil
}

func (a *kneeAdapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return &adapters.QueryResult{Found: false}, nil
}

func init() {
	adapters.Register("kneemock", func() adapters.PlatformAdapter { return &kneeAdapter{} })
}

const kneeProfile = `
name: knee
budget:
  total_cpus: 4
  total_memory_gb: 4
platforms:
  kneemock:
    nodes: {node: 1}
    per_container: {cpus: 1.0, memory: "1g"}
`

func writeKneeProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "knee.yaml"), []byte(kneeProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runKneeSweep(t *testing.T, steps, abortAfter string, kneeTPS int) *harness.RunResult {
	t.Helper()
	outDir := t.TempDir()
	cfgYAML := `
name: unit-knee
platform: kneemock
workload: kv-write
profile: knee
normalized: true
load:
  mode: open-loop
  key_distribution: uniform
  key_space: 1000
  finality_wait: 5s
  sweep:
    enabled: true
    probe_tps: 10
    probe_duration: 600ms
    steps: ` + steps + `
    step_duration: 1200ms
    hold_fraction: 0.9
    hold_duration: 800ms
    max_fail_rate: 0.02
    abort_after_failed_steps: ` + abortAfter + `
metrics:
  warmup: 200ms
  cooldown: 200ms
  output_dir: ` + outDir + `
adapter:
  knee_tps: ` + fmt.Sprint(kneeTPS) + `
`
	cfgPath := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := harness.LoadRunConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rr, err := harness.Engine{}.Run(ctx, cfg, harness.Options{ProfileDir: writeKneeProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	return rr
}

func phaseByName(rr *harness.RunResult, name string) *harness.PhaseResult {
	for i := range rr.Phases {
		if rr.Phases[i].Name == name {
			return &rr.Phases[i]
		}
	}
	return nil
}

// The hold phase is the headline result, so it must be offered at hold_fraction
// of the step that actually held - not of the top of the configured ladder.
// Before this was fixed the hold always ran at 0.9 x the top step, which put the
// headline deep into overload for any platform that saturated early.
func TestSweepHoldFollowsMeasuredKnee(t *testing.T) {
	rr := runKneeSweep(t, "[50, 100, 400, 800]", "0", 150)

	if rr.SaturationTPS != 100 {
		t.Fatalf("expected saturation at the 100 TPS step, got %d (phases: %s)", rr.SaturationTPS, phaseSummary(rr))
	}
	hold := phaseByName(rr, "hold")
	if hold == nil {
		t.Fatal("no hold phase")
	}
	if want := 90; hold.OfferedTPS != want {
		t.Errorf("hold offered %d TPS, want %d (0.9 x measured knee 100), not 720 (0.9 x top step 800)",
			hold.OfferedTPS, want)
	}
}

// Early abort is what makes one shared ladder viable across platforms with very
// different ceilings: a platform that saturates low stops climbing instead of
// spending the rest of the run failing every remaining step.
func TestSweepEarlyAbortSkipsRemainingSteps(t *testing.T) {
	rr := runKneeSweep(t, "[50, 100, 400, 800, 1600]", "2", 150)

	// 400 is the first failure and 800 the second; the abort trips only once two
	// consecutive failures have actually been observed, so 1600 is the first step
	// never offered.
	for _, name := range []string{"sweep-400", "sweep-800"} {
		if phaseByName(rr, name) == nil {
			t.Errorf("%s should have run: the abort trips only after 2 observed failures", name)
		}
	}
	if phaseByName(rr, "sweep-1600") != nil {
		t.Error("sweep-1600 ran; expected it to be skipped after 2 consecutive failed steps")
	}
	want := []int{1600}
	if got := rr.Manifest.SkippedSteps; len(got) != len(want) {
		t.Fatalf("skipped_steps = %v, want %v", got, want)
	}
	for i, v := range want {
		if rr.Manifest.SkippedSteps[i] != v {
			t.Fatalf("skipped_steps = %v, want %v", rr.Manifest.SkippedSteps, want)
		}
	}
	// A truncated ladder must be self-describing in the manifest, or the run
	// looks like it was configured with a shorter ladder.
	if !hasCaveatContaining(rr.Manifest.Caveats, "sweep aborted early") {
		t.Errorf("no early-abort caveat recorded; caveats=%v", rr.Manifest.Caveats)
	}
	// The knee is still found and the hold still follows it.
	if rr.SaturationTPS != 100 {
		t.Errorf("saturation = %d, want 100", rr.SaturationTPS)
	}
	if hold := phaseByName(rr, "hold"); hold == nil || hold.OfferedTPS != 90 {
		t.Errorf("hold offered %v, want 90", hold)
	}
}

// A platform that never holds a single step has no knee to sit below. Holding at
// 0.9 x the top step there would report a headline from total overload, so the
// hold falls back to the probe rate and the run says so.
func TestSweepNoPassingStepFallsBackToProbe(t *testing.T) {
	rr := runKneeSweep(t, "[400, 800, 1600]", "2", 5)

	if rr.SaturationTPS != 0 {
		t.Fatalf("expected no passing step, got saturation %d", rr.SaturationTPS)
	}
	hold := phaseByName(rr, "hold")
	if hold == nil {
		t.Fatal("no hold phase")
	}
	if hold.OfferedTPS != 10 {
		t.Errorf("hold offered %d TPS, want the probe rate 10", hold.OfferedTPS)
	}
	if !hasCaveatContaining(rr.Manifest.Caveats, "no sweep step held") {
		t.Errorf("no floor-reading caveat recorded; caveats=%v", rr.Manifest.Caveats)
	}
}

func hasCaveatContaining(caveats []string, sub string) bool {
	for _, c := range caveats {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func phaseSummary(rr *harness.RunResult) string {
	out := ""
	for _, p := range rr.Phases {
		out += fmt.Sprintf("%s(offered=%d fail=%.3f) ", p.Name, p.OfferedTPS, p.Result.FailureRate)
	}
	return out
}

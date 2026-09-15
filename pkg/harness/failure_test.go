package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/harness"
)

// loadMockConfig writes a short open-loop mock run config with extra YAML
// appended under adapter: and returns it loaded.
func loadMockConfig(t *testing.T, outDir, hold, adapterYAML string) *harness.RunConfig {
	t.Helper()
	body := `
name: unit-failure
platform: mock
workload: kv-write
profile: test
load:
  mode: open-loop
  target_tps: 100
  hold_duration: ` + hold + `
  key_space: 100
  finality_wait: 2s
metrics:
  warmup: 200ms
  cooldown: 100ms
  output_dir: ` + outDir + `
adapter:
` + adapterYAML
	p := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := harness.LoadRunConfig(p)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestSetupFailureLeavesErrorFileWithHints(t *testing.T) {
	outDir := t.TempDir()
	cfg := loadMockConfig(t, outDir, "1s", "  setup_error: \"peer unreachable\"\n")
	_, err := harness.Engine{}.Run(context.Background(), cfg, harness.Options{ProfileDir: writeProfile(t)})
	if err == nil || !strings.Contains(err.Error(), "peer unreachable") || !strings.Contains(err.Error(), "mock") {
		t.Fatalf("setup error should name the platform and cause, got %v", err)
	}
	m, _ := filepath.Glob(filepath.Join(outDir, "mock", "*", "error.txt"))
	if len(m) != 1 {
		t.Fatalf("expected one error.txt, got %v", m)
	}
	b, _ := os.ReadFile(m[0])
	for _, want := range []string{"peer unreachable", "connection.env", "docs/README.md#troubleshooting"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("error.txt missing %q:\n%s", want, b)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(m[0]), "run.log")); err != nil {
		t.Errorf("run.log should exist next to error.txt: %v", err)
	}
}

func TestAllSubmitsFailingIsRecordedAndCountedPerGenerator(t *testing.T) {
	outDir := t.TempDir()
	cfg := loadMockConfig(t, outDir, "1500ms", "  fail_rate: 1\n  submit_ms: 0\n  jitter_ms: 0\n")
	rr, err := harness.Engine{}.Run(context.Background(), cfg, harness.Options{ProfileDir: writeProfile(t), Generators: 3})
	if err != nil {
		t.Fatalf("an all-failed run still completes and writes results: %v", err)
	}
	h := rr.Headline
	if h == nil || h.Committed != 0 {
		t.Fatalf("headline = %+v, want present with 0 committed", h)
	}
	if h.Errored != h.Submitted || h.Submitted < 60 {
		// 3 generators at ~33 TPS for ~1.2 s window. Colliding synthetic ids across
		// generators used to overwrite records and shrink submitted.
		t.Errorf("submitted=%d errored=%d: every offered tx must be its own failure record", h.Submitted, h.Errored)
	}
	if len(h.Errors) == 0 || !strings.Contains(h.Errors[0].Message, "fail_rate") {
		t.Errorf("failure reason not grouped: %+v", h.Errors)
	}
	log, err := os.ReadFile(filepath.Join(rr.OutDir, "run.log"))
	if err != nil || !strings.Contains(string(log), "FAILED RUN") {
		t.Errorf("run.log should flag the failed run: err=%v\n%s", err, log)
	}
}

func TestInterruptedRunKeepsPartialPhaseWithCaveat(t *testing.T) {
	outDir := t.TempDir()
	cfg := loadMockConfig(t, outDir, "10s", "  submit_ms: 1\n  commit_ms: 5\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(700*time.Millisecond, cancel)
	start := time.Now()
	rr, err := harness.Engine{}.Run(ctx, cfg, harness.Options{ProfileDir: writeProfile(t)})
	if err != nil {
		t.Fatalf("interrupted run should still write results: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("run did not stop promptly on cancel")
	}
	if rr.Headline != nil {
		t.Error("an interrupted phase must not be a headline")
	}
	if len(rr.Phases) != 1 || rr.Phases[0].Verdict != "interrupted" {
		t.Fatalf("phases = %+v, want one interrupted phase", rr.Phases)
	}
	found := false
	for _, c := range rr.Manifest.Caveats {
		if strings.Contains(c, "run interrupted") {
			found = true
		}
	}
	if !found {
		t.Errorf("no interrupt caveat in %q", rr.Manifest.Caveats)
	}
	if _, err := os.Stat(filepath.Join(rr.OutDir, "summary.txt")); err != nil {
		t.Errorf("summary.txt not written: %v", err)
	}
}

func TestSystemMetricsDisabledIsCaveated(t *testing.T) {
	outDir := t.TempDir()
	cfg := loadMockConfig(t, outDir, "500ms", "  submit_ms: 1\n")
	rr, err := harness.Engine{}.Run(context.Background(), cfg, harness.Options{ProfileDir: writeProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rr.Manifest.Caveats {
		if strings.Contains(c, "system_metrics disabled") {
			return
		}
	}
	t.Errorf("missing system_metrics caveat: %q", rr.Manifest.Caveats)
}

package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/harness"

	_ "github.com/juicedcore/bench/pkg/adapters/mock"
)

const testProfile = `
name: test
budget:
  total_cpus: 11
  total_memory_gb: 11
platforms:
  mock:
    nodes: {node: 1}
    per_container: {cpus: 1.0, memory: "1g"}
    orderer_batch:
      max_message_count: 100
      batch_timeout: "1s"
      preferred_max_bytes: "512KB"
      absolute_max_bytes: "10MB"
`

func writeProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(testProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEngineOpenLoopRun(t *testing.T) {
	profDir := writeProfile(t)
	outDir := t.TempDir()

	cfgYAML := `
name: unit-open-loop
platform: mock
workload: kv-write
profile: test
normalized: true
load:
  mode: open-loop
  target_tps: 200
  hold_duration: 3s
  key_distribution: uniform
  key_space: 1000
  finality_wait: 5s
metrics:
  warmup: 500ms
  cooldown: 300ms
  output_dir: ` + outDir + `
adapter:
  submit_ms: 1
  commit_ms: 20
  jitter_ms: 5
`
	cfgPath := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := harness.LoadRunConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rr, err := harness.Engine{}.Run(ctx, cfg, harness.Options{ProfileDir: profDir})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if rr.Headline == nil {
		t.Fatal("no headline result")
	}
	h := rr.Headline
	if !h.InvariantOK {
		t.Errorf("invariant broken: submitted=%d committed=%d invalid=%d errored=%d timedout=%d",
			h.Submitted, h.Committed, h.Invalid, h.Errored, h.TimedOut)
	}
	if h.Committed == 0 {
		t.Fatal("no committed transactions")
	}
	// ~200 TPS offered for ~2.2s measured window => expect well over 100 confirmed.
	if h.ConfirmedTPS < 100 {
		t.Errorf("confirmed TPS too low: %.1f", h.ConfirmedTPS)
	}
	// mock commit latency ~20ms + jitter; e2e p50 should be in a sane band.
	if p50 := h.E2E.Percentiles["p50"]; p50 < 5 || p50 > 200 {
		t.Errorf("e2e p50 out of expected band: %.2f ms", p50)
	}

	// Manifest must capture the fairness levers.
	man := readManifest(t, outDir)
	if man.StateDB != "leveldb" {
		t.Errorf("normalized run must pin leveldb, got %q", man.StateDB)
	}
	if man.OrdererBatch.MaxMessageCount != 100 {
		t.Errorf("orderer batch params not recorded: %+v", man.OrdererBatch)
	}
	if man.Seed == 0 {
		t.Error("seed not recorded")
	}
}

func TestEngineClosedLoopRun(t *testing.T) {
	profDir := writeProfile(t)
	outDir := t.TempDir()
	cfgYAML := `
name: unit-closed-loop
platform: mock
workload: transfer
profile: test
load:
  mode: closed-loop
  workers: 8
  hold_duration: 2s
  key_distribution: zipfian
  zipfian_constant: 1.2
  key_space: 200
  finality_wait: 5s
metrics:
  warmup: 300ms
  cooldown: 200ms
  output_dir: ` + outDir + `
adapter:
  submit_ms: 1
  commit_ms: 10
  conflict_rate: 0.1
`
	cfgPath := filepath.Join(t.TempDir(), "run.yaml")
	os.WriteFile(cfgPath, []byte(cfgYAML), 0o644)
	cfg, err := harness.LoadRunConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rr, err := harness.Engine{}.Run(ctx, cfg, harness.Options{ProfileDir: profDir})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Headline == nil || rr.Headline.Committed == 0 {
		t.Fatal("expected committed transactions in closed-loop run")
	}
	if !rr.Headline.InvariantOK {
		t.Error("invariant broken in closed-loop run")
	}
	// conflict_rate 0.1 on writes only; transfer maps to a write-path tx in mock,
	// so we expect a non-zero failure rate but well under half.
	if fr := rr.Headline.FailureRate; fr <= 0 || fr > 0.5 {
		t.Errorf("unexpected failure rate %.3f", fr)
	}
}

func TestEngineMultiGenerator(t *testing.T) {
	profDir := writeProfile(t)
	outDir := t.TempDir()
	cfgYAML := `
name: unit-multigen
platform: mock
workload: kv-write
profile: test
normalized: true
load:
  mode: open-loop
  target_tps: 400
  hold_duration: 3s
  key_distribution: uniform
  key_space: 2000
  finality_wait: 5s
metrics: { warmup: 400ms, cooldown: 300ms, output_dir: ` + outDir + ` }
adapter: { submit_ms: 1, commit_ms: 15, jitter_ms: 4 }
`
	cfgPath := filepath.Join(t.TempDir(), "run.yaml")
	os.WriteFile(cfgPath, []byte(cfgYAML), 0o644)
	cfg, err := harness.LoadRunConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rr, err := harness.Engine{}.Run(ctx, cfg, harness.Options{ProfileDir: profDir, Generators: 4})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rr.Headline == nil || !rr.Headline.InvariantOK {
		t.Fatalf("multi-gen run invariant broken: %+v", rr.Headline)
	}
	// 4 gens x ~100 TPS each ~= 400 offered; expect >250 confirmed in the window.
	if rr.Headline.ConfirmedTPS < 250 {
		t.Errorf("multi-gen confirmed TPS too low: %.1f", rr.Headline.ConfirmedTPS)
	}
	man := readManifest(t, outDir)
	if man.Generators != 4 {
		t.Errorf("manifest.generators = %d, want 4", man.Generators)
	}
	// An explicit count that differs from the profile default changes the key
	// sequence (generator i uses seed+i), so a normalized run must say so.
	found := false
	for _, c := range man.Caveats {
		if strings.Contains(c, "generator count overridden to 4") {
			found = true
		}
	}
	if !found {
		t.Errorf("manifest missing generator-override caveat: %v", man.Caveats)
	}
}

func readManifest(t *testing.T, outDir string) harness.Manifest {
	t.Helper()
	var found string
	filepath.WalkDir(outDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "manifest.json" {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("manifest.json not written")
	}
	b, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	var m harness.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

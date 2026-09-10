package harness

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigDefaultsAndDurations(t *testing.T) {
	c, err := LoadRunConfig(write(t, `
name: x
platform: fabric-cft
workload: kv-write
load:
  mode: open-loop
  target_tps: 100
  hold_duration: 90s
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Load.KeySpace != 100000 || c.Load.ValueSizeBytes != 64 || c.Load.Seed != 1 {
		t.Errorf("defaults not applied: %+v", c.Load)
	}
	if c.Metrics.Warmup.D() != 30*time.Second || c.Metrics.Cooldown.D() != 15*time.Second {
		t.Errorf("warmup/cooldown defaults wrong: %v / %v", c.Metrics.Warmup.D(), c.Metrics.Cooldown.D())
	}
	if c.Load.HoldDur.D() != 90*time.Second {
		t.Errorf("duration parse: got %v", c.Load.HoldDur.D())
	}
}

func TestConfigValidation(t *testing.T) {
	cases := map[string]string{
		"missing platform": `
name: x
workload: kv-write
load: {mode: open-loop, target_tps: 10, hold_duration: 1s}`,
		"missing workload": `
name: x
platform: fabric-cft
load: {mode: open-loop, target_tps: 10, hold_duration: 1s}`,
		"closed-loop no workers": `
name: x
platform: fabric-cft
workload: kv-write
load: {mode: closed-loop, hold_duration: 1s}`,
		"open-loop no rate": `
name: x
platform: fabric-cft
workload: kv-write
load: {mode: open-loop, hold_duration: 1s}`,
		"bad mode": `
name: x
platform: fabric-cft
workload: kv-write
load: {mode: sideways, target_tps: 10, hold_duration: 1s}`,
	}
	for name, body := range cases {
		if _, err := LoadRunConfig(write(t, body)); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestConfigEnvExpansion(t *testing.T) {
	t.Setenv("BENCH_TEST_ENDPOINT", "localhost:7051")
	c, err := LoadRunConfig(write(t, `
name: x
platform: fabric-cft
workload: kv-write
load: {mode: open-loop, target_tps: 10, hold_duration: 1s}
adapter:
  peer_endpoint: "${BENCH_TEST_ENDPOINT}"
  missing: "${BENCH_TEST_NOT_SET}"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Adapter["peer_endpoint"] != "localhost:7051" {
		t.Errorf("env not expanded: %v", c.Adapter["peer_endpoint"])
	}
	if c.Adapter["missing"] != "" {
		t.Errorf("undefined var should expand to empty, got %q", c.Adapter["missing"])
	}
}

func TestSweepDefaults(t *testing.T) {
	c, err := LoadRunConfig(write(t, `
name: x
platform: fabric-cft
workload: kv-write
load:
  mode: open-loop
  sweep:
    enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Load.Sweep
	if s.ProbeTPS != 10 || s.ProbeDur.D() != 30*time.Second || s.StepDur.D() != 60*time.Second {
		t.Errorf("sweep probe/step defaults wrong: %+v", s)
	}
	if s.HoldFrac != 0.9 || s.MaxFailRate != 0.02 || len(s.Steps) == 0 {
		t.Errorf("sweep hold/fail/steps defaults wrong: %+v", s)
	}
}

func TestBuildPhasesSweep(t *testing.T) {
	c := &RunConfig{}
	c.Load.Mode = "open-loop"
	c.Load.Sweep = SweepConfig{Enabled: true, ProbeTPS: 10, Steps: []int{100, 1000}}
	c.applyDefaults()
	phases := buildPhases(c)
	// probe + 2 sweep + hold
	if len(phases) != 4 {
		t.Fatalf("expected 4 phases, got %d: %+v", len(phases), phases)
	}
	if phases[0].name != "probe" || phases[len(phases)-1].name != "hold" {
		t.Errorf("phase order wrong: %s ... %s", phases[0].name, phases[len(phases)-1].name)
	}
}

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStrictRejections(t *testing.T) {
	base := "name: x\nplatform: mock\nworkload: kv-write\n"
	cases := map[string]struct{ body, want string }{
		"typo key":             {base + "load: {mode: open-loop, target_tps: 10, hold_duraton: 1s}", "hold_duraton"},
		"steps not ascending":  {base + "load: {sweep: {enabled: true, steps: [100, 50]}}", "strictly ascending"},
		"fail rate as percent": {base + "load: {sweep: {enabled: true, max_fail_rate: 2}}", "max_fail_rate"},
		"target and ramp":      {base + "load: {target_tps: 10, ramp_to: 100}", "ramp_to"},
		"bad distribution":     {base + "load: {target_tps: 10, key_distribution: zipf}", "uniform, zipfian, fixed"},
		"sweep closed loop":    {base + "load: {mode: closed-loop, workers: 2, sweep: {enabled: true}}", "open-loop methodology"},
		"negative duration":    {base + "load: {target_tps: 10, finality_wait: -1s}", "finality_wait"},
		"bad output format":    {base + "load: {target_tps: 10}\nmetrics: {output_format: CSV}", "output_format"},
	}
	for name, tc := range cases {
		p := write(t, tc.body)
		_, err := LoadRunConfig(p)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: error should name %q and the file, got: %v", name, tc.want, err)
		}
	}
}

func TestConfigExplicitZeroFailRateIsHonoured(t *testing.T) {
	c, err := LoadRunConfig(write(t, "name: x\nplatform: mock\nworkload: kv-write\nload: {sweep: {enabled: true, max_fail_rate: 0}}"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Load.Sweep.MaxFailRate != 0 {
		t.Errorf("explicit max_fail_rate: 0 replaced by default %g", c.Load.Sweep.MaxFailRate)
	}
}

func TestShippedProfilesLoadStrictly(t *testing.T) {
	m, _ := filepath.Glob("../../deploy/profiles/*.yaml")
	if len(m) == 0 {
		t.Fatal("no profiles found")
	}
	for _, f := range m {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if _, err := LoadProfile(name, "../../deploy/profiles"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestProfileRejections(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("nobudget", "platforms:\n  mock:\n    nodes: {node: 1}\n")
	put("typo", "budget: {total_cpus: 1, total_memory_gb: 1}\nplatfroms: {}\n")
	put("nobatch", "budget: {total_cpus: 1, total_memory_gb: 1}\nplatforms:\n  fabric-cft:\n    nodes: {peer: 1}\n")
	put("anchors", "budget: {total_cpus: 1, total_memory_gb: 1}\n_shared: &s {node: 1}\nplatforms:\n  mock:\n    nodes: *s\n")
	for name, want := range map[string]string{"nobudget": "total_cpus", "typo": "platfroms", "nobatch": "max_message_count", "missing": "available: anchors"} {
		_, err := LoadProfile(name, dir)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want error containing %q, got %v", name, want, err)
		}
	}
	if _, err := LoadProfile("anchors", dir); err != nil {
		t.Errorf("_-prefixed anchor keys must be allowed: %v", err)
	}
	if _, err := LoadProfile("", dir); err == nil {
		t.Error("empty profile name must be an error")
	}
}

func TestCSVWriteFailureIsReported(t *testing.T) {
	if err := writePhaseCSV(filepath.Join(t.TempDir(), "missing-dir", "phases.csv"), &RunResult{}); err == nil {
		t.Error("expected an error writing into a missing directory")
	}
}

func TestReportMissingDirAndCorruptResult(t *testing.T) {
	if err := BuildReport(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "r.html"), ReportOptions{}); err == nil || IsIncompleteReport(err) {
		t.Errorf("missing results dir must be a hard error, got %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "mock", "1", "result.json")
	_ = os.MkdirAll(filepath.Dir(bad), 0o755)
	_ = os.WriteFile(bad, []byte("{truncated"), 0o644)
	out := filepath.Join(t.TempDir(), "r.html")
	err := BuildReport(dir, out, ReportOptions{})
	if !IsIncompleteReport(err) {
		t.Fatalf("corrupt result.json should give an incomplete-report warning, got %v", err)
	}
	html, _ := os.ReadFile(out)
	if !strings.Contains(string(html), bad) {
		t.Error("report page should list the unreadable file")
	}
}

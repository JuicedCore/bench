package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every run folder gets a page - completed, platform-failed and aborted alike -
// and hostile strings from result files are escaped.
func TestRunbookPagesEveryRun(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	ok := RunResult{
		Manifest: Manifest{RunName: "probe-sweep", Platform: "mock", Profile: "local", StartedAt: start, EndedAt: start.Add(time.Minute), Generators: 1, ResourceContainers: 1},
		Phases: []PhaseResult{
			{Name: "sweep-100", OfferedTPS: 100, Verdict: "held", Result: metrics.Result{Submitted: 100, Committed: 100, ConfirmedTPS: 100, InvariantOK: true}},
			{Name: "sweep-200", OfferedTPS: 200, Verdict: "fail", Result: metrics.Result{Submitted: 200, Committed: 150, ConfirmedTPS: 150, Errors: []metrics.ErrorCount{{Message: "<script>alert(1)</script>", Count: 50}}}},
		},
	}
	writeJSON(t, filepath.Join(root, "mock", "20260915-120000", "result.json"), ok)

	failed := ok
	failed.Manifest.StartedAt = start.Add(time.Hour)
	failed.Manifest.ContainerFailures = []metrics.ContainerFailure{{Name: "node-0", Exited: true, ExitCode: 139}}
	writeJSON(t, filepath.Join(root, "mock", "20260915-130000", "result.json"), failed)

	writeJSON(t, filepath.Join(root, "mock", "20260915-140000", "manifest.json"), Manifest{RunName: "quick-smoke", Platform: "mock", StartedAt: start.Add(2 * time.Hour)})
	if err := os.MkdirAll(filepath.Join(root, "mock", "20260915-150000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "_campaigns", "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(root, RunbookFile)
	if err := BuildRunbook(root, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, want := range []string{
		`id="mock-20260915-120000"`, `id="mock-20260915-130000"`, `id="mock-20260915-140000"`,
		"Platform failure", "Aborted before results", "1 empty run folder",
		"&lt;script&gt;alert(1)&lt;/script&gt;", "Throughput by phase",
		`href="mock/20260915-120000/result.json"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("run book is missing %q", want)
		}
	}
	if strings.Contains(page, "<script>alert(1)") {
		t.Error("error message was not escaped")
	}
	// Newest first.
	if strings.Index(page, `data-run="mock-20260915-140000"`) > strings.Index(page, `data-run="mock-20260915-120000"`) {
		t.Error("runs are not ordered newest first")
	}
}

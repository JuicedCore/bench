package logx

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"": slog.LevelInfo, "DEBUG": slog.LevelDebug, "warning": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil || !strings.Contains(err.Error(), Levels) {
		t.Errorf("bad level error should list valid levels, got %v", err)
	}
}

func TestWithFileCapturesDebugWhileBaseStaysAtInfo(t *testing.T) {
	var term bytes.Buffer
	base := slog.New(slog.NewTextHandler(&term, &slog.HandlerOptions{Level: slog.LevelInfo}))
	path := filepath.Join(t.TempDir(), "run.log")
	l, closeFn, err := WithFile(base, path)
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("detail")
	l.Warn("problem")
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "detail") || !strings.Contains(string(b), "problem") {
		t.Errorf("run.log missing lines: %s", b)
	}
	if strings.Contains(term.String(), "detail") || !strings.Contains(term.String(), "problem") {
		t.Errorf("terminal should have warn only: %s", term.String())
	}
}

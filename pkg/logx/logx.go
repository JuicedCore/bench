// Package logx sets up the harness's structured logging. Everything diagnostic -
// phase boundaries, swallowed-but-recorded errors, caveats as they are raised -
// goes through log/slog to stderr, and a run additionally tees it into
// <result dir>/run.log so the reason a run went wrong travels with its results.
// Normal output (progress, "results written to") stays on stdout/stderr as plain
// text and is not affected by the level.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Levels lists the accepted --log-level values.
const Levels = "debug|info|warn|error"

// ParseLevel maps a --log-level / BENCH_LOG_LEVEL value to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want %s)", s, Levels)
}

// DefaultLevel is BENCH_LOG_LEVEL when set and valid, otherwise info. Campaign
// scripts export it so every benchrunner they start logs at the same level.
func DefaultLevel() string {
	if v := os.Getenv("BENCH_LOG_LEVEL"); v != "" {
		if _, err := ParseLevel(v); err == nil {
			return v
		}
	}
	return "info"
}

// Setup installs a text logger on w at the given level as the slog default.
func Setup(w io.Writer, level string) error {
	lv, err := ParseLevel(level)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv})))
	return nil
}

// WithFile returns a logger that writes to both base's handler and a file at
// path. The file always records debug and above, so run.log holds the full
// story even when the terminal was kept at info. The returned close function
// flushes and closes the file; it is safe to call more than once.
func WithFile(base *slog.Logger, path string) (*slog.Logger, func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return base, func() error { return nil }, fmt.Errorf("open run log %s: %w", path, err)
	}
	fh := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	closed := false
	return slog.New(slog.NewMultiHandler(base.Handler(), fh)), func() error {
		if closed {
			return nil
		}
		closed = true
		return f.Close()
	}, nil
}

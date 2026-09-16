// benchrunner is the CLI entry point for the unified blockchain benchmark harness.
//
//	benchrunner run      --config configs/normalized/quick-smoke.yaml [--platform X] [--dry-run]
//	benchrunner suite    --configs configs/ --platforms a,b,c [--profile local]
//	benchrunner report   --results-dir ./results --output ./report.html
//	benchrunner setup    --platform fabric-cft --profile local
//	benchrunner teardown --platform fabric-cft
//	benchrunner list
//
// Every command accepts --log-level debug|info|warn|error (default info, or
// BENCH_LOG_LEVEL). Diagnostics go to stderr; `run` also keeps them, at debug
// level, in <result dir>/run.log.
//
// exit codes: 0 ok, 1 error, 2 usage error, 3 a platform container failed
// mid-run (results still written).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/harness"
	"github.com/juicedcore/bench/pkg/logx"
	"github.com/juicedcore/bench/pkg/monitoring"

	// Register adapters. All five are fully implemented; fabricx and neuchain are
	// blocked by their platforms (a devnet namespace-bootstrap failure and an
	// unbuilt C++ image respectively), not by the adapter code.
	_ "github.com/juicedcore/bench/pkg/adapters/drunix"
	_ "github.com/juicedcore/bench/pkg/adapters/fabric"
	_ "github.com/juicedcore/bench/pkg/adapters/fabricx"
	_ "github.com/juicedcore/bench/pkg/adapters/mock"
	_ "github.com/juicedcore/bench/pkg/adapters/neuchain"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if err := logx.Setup(os.Stderr, logx.DefaultLevel()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	// The first Ctrl-C/SIGTERM cancels the run so it stops cleanly and writes
	// partial results; the handler is then removed, so a second one kills a
	// drain that is stuck instead of being swallowed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		signal.Stop(sigCh)
		slog.Warn("signal received: stopping load, draining in-flight transactions and writing partial results (send it again to abort immediately)", "signal", sig.String())
		cancel()
	}()

	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "suite":
		err = cmdSuite(ctx, os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "runbook":
		err = cmdRunbook(os.Args[2:])
	case "setup":
		err = cmdDeploy(ctx, os.Args[2:], "up")
	case "teardown":
		err = cmdDeploy(ctx, os.Args[2:], "down")
	case "list":
		fmt.Println("registered adapters:", strings.Join(adapters.Registered(), ", "))
		if dirs := deployDirs(); len(dirs) > 0 {
			fmt.Println("deployable (deploy/docker):", strings.Join(dirs, ", "))
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		if errors.Is(err, errPlatformFailure) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

// errUsage marks a command-line mistake (exit 2, like flag parse errors).
var errUsage = errors.New("usage")

// parseFlags adds --log-level to fs, parses args, applies the level, and rejects
// stray positional arguments (a mistyped "--config=x y" would otherwise be
// silently ignored).
func parseFlags(fs *flag.FlagSet, args []string) error {
	level := fs.String("log-level", logx.DefaultLevel(), "diagnostic log level: "+logx.Levels)
	_ = fs.Parse(args) // flag.ExitOnError: a bad flag already exited 2 with usage
	if err := logx.Setup(os.Stderr, *level); err != nil {
		return fmt.Errorf("%w: --log-level: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: %s: unexpected argument(s) %q (flags take the form --name value)", errUsage, fs.Name(), fs.Args())
	}
	return nil
}

// errPlatformFailure marks a run that completed and wrote its results but lost a
// platform container mid-run. main exits 3 for it so campaign scripts can tell it
// apart from a harness error (exit 1).
var errPlatformFailure = errors.New("platform container failed during the run")

func usage() {
	fmt.Print(`benchrunner - unified blockchain benchmark harness

commands:
  run       run a single benchmark from a YAML config
  suite     run every config in a directory across one or more platforms
  report    build an HTML comparison from a results directory
  runbook   build the run book: one HTML page per run (rebuilt after every run)
  setup     bring a platform's docker-compose topology up (for a profile)
  teardown  bring a platform's topology down
  list      list registered platform adapters

every command accepts --log-level debug|info|warn|error (env BENCH_LOG_LEVEL)

exit codes:
  0  ok
  1  error (message on stderr; for run, also error.txt / run.log in the result dir)
  2  usage error
  3  a platform container failed mid-run (results still written)

troubleshooting: docs/README.md#troubleshooting
`)
}

// isTerminal reports whether f looks like an interactive terminal rather than
// a pipe or redirected file - a char-device stat is the standard stdlib-only
// heuristic for this (avoids pulling in golang.org/x/term for one check).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// resolveProgress decides where (if anywhere) live progress output goes.
// quiet always wins. Otherwise: a terminal on stderr gets redraw-in-place
// output; a non-terminal (piped/redirected) stderr stays silent unless force
// (--progress) is set, in which case it gets one plain line per tick instead
// of raw carriage returns, so a log file stays readable.
func resolveProgress(quiet, force bool) (io.Writer, bool) {
	if quiet {
		return nil, false
	}
	if isTerminal(os.Stderr) {
		return os.Stderr, true
	}
	if force {
		return os.Stderr, false
	}
	return nil, false
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to run config YAML (required)")
	platform := fs.String("platform", "", "override config.platform")
	profile := fs.String("profile", "", "override config.profile")
	profileDir := fs.String("profile-dir", "", "directory holding profile YAMLs (default deploy/profiles)")
	dryRun := fs.Bool("dry-run", false, "print the phase plan without generating load")
	caveat := fs.String("caveat", "", "append a caveat string to the manifest")
	generators := fs.Int("generators", 0, "load-generator instances sharing the adapter (default: one per load_gen_cpus in the profile; overriding it on a normalized run changes the key sequence and is caveated)")
	quiet := fs.Bool("quiet", false, "disable live progress output")
	progress := fs.Bool("progress", false, "force live progress output even when stderr is not a terminal (one plain line per tick, log-safe)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *cfgPath == "" {
		return fmt.Errorf("%w: run: --config is required (e.g. --config configs/normalized/quick-smoke.yaml)", errUsage)
	}

	cfg, err := harness.LoadRunConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *platform != "" {
		cfg.Platform = *platform
	}
	if *profile != "" {
		cfg.Profile = *profile
	}

	progW, progTTY := resolveProgress(*quiet, *progress)
	opt := harness.Options{ProfileDir: *profileDir, DryRun: *dryRun, Generators: *generators, Progress: progW, ProgressTTY: progTTY}
	if *caveat != "" {
		opt.Caveats = append(opt.Caveats, *caveat)
	}
	// Standard caveat for resource-starved scale-out platforms on local.
	if cfg.Profile == "local" && (cfg.Platform == "fabricx" || cfg.Platform == "neuchain") {
		opt.Caveats = append(opt.Caveats,
			cfg.Platform+" on the local profile is resource-constrained; absolute throughput is NOT comparable to its published scale-out ceiling")
	}
	// Drunix's shipped test-network runs on YugabyteDB; LevelDB parity is not
	// available (see deploy/docker/drunix/up.sh, adr-012). Disclose it on
	// normalized runs where the harness would otherwise imply LevelDB.
	if cfg.Platform == "drunix" && cfg.Normalized {
		opt.Caveats = append(opt.Caveats,
			"drunix ran on YugabyteDB (its shipped test-network default); LevelDB parity is unavailable, so the state-DB variable is NOT held constant vs fabric-cft for this run")
	}
	// Drunix's YugabyteDB statedb force-casts every write value into a JSONB
	// column and panics the Committing Peer on non-JSON values (see
	// docs/platforms/drunix.md). The drunix adapter JSON-wraps TxWrite values
	// client-side to survive this; disclose it since the on-wire payload no
	// longer matches the shared kv workload's raw bytes on other platforms.
	if cfg.Platform == "drunix" {
		opt.Caveats = append(opt.Caveats,
			"drunix write values are JSON-wrapped client-side to survive a YugabyteDB statedb bug (non-JSON values panic the Committing Peer); the on-wire payload format differs from the shared kv workload on other platforms for this run")
	}
	if !cfg.Normalized {
		switch cfg.Platform {
		case "fabricx":
			opt.Caveats = append(opt.Caveats,
				"fabricx native run uses the gRPC path (adr-016); Token SDK Issue/Transfer/Redeem is not implemented (the old REST token configs were removed)")
		case "neuchain":
			opt.Caveats = append(opt.Caveats,
				"neuchain native run is shortened to stay under the upstream epoch-10000 crash (nodes SIGSEGV together; see docs/REMAINING-WORK.md)")
		}
		if cfg.Workload == "kv-mixed" {
			opt.Caveats = append(opt.Caveats,
				"kv-mixed native: Fabric/Drunix/Fabric-X reads are Evaluate/QueryService (not ordered); NeuChain reads are committed transactions. Headline TPS blends two paths.")
		}
	}

	if !*dryRun {
		// Deferred so a run that fails still gets its page (error.txt, manifest).
		defer updateRunbook(cfg.Metrics.OutputDir)
	}
	rr, err := harness.Engine{}.Run(ctx, cfg, opt)
	if err != nil {
		return err
	}
	if !*dryRun {
		generateMonitoringReport(cfg, rr)
	}
	if len(rr.Manifest.ContainerFailures) > 0 {
		parts := make([]string, len(rr.Manifest.ContainerFailures))
		for i, f := range rr.Manifest.ContainerFailures {
			parts[i] = f.String()
		}
		return fmt.Errorf("%w: %s (results written to %s)", errPlatformFailure, strings.Join(parts, "; "), rr.OutDir)
	}
	return nil
}

// generateMonitoringReport writes the per-run monitoring HTML report. Failures
// here are logged, never fatal - a run that succeeded must not be reported as
// failed just because Prometheus was unreachable or a chart didn't render.
//
// It deliberately does not use the run's context: after Ctrl-C that context is
// cancelled, and the report would claim Prometheus was unreachable when the
// real story is the partial run it documents.
func generateMonitoringReport(cfg *harness.RunConfig, rr *harness.RunResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := monitoring.GenerateReport(ctx, cfg, rr); err != nil {
		slog.Warn("monitoring report not written (the run's results are unaffected)", "dir", rr.OutDir, "err", err)
		return
	}
	fmt.Println("monitoring report:", filepath.Join(rr.OutDir, "monitoring-report.html"))
}

func cmdSuite(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("suite", flag.ExitOnError)
	dir := fs.String("configs", "configs", "directory of run config YAMLs")
	platforms := fs.String("platforms", "", "comma-separated platform list (default: each config's own)")
	profile := fs.String("profile", "", "override profile for all runs")
	profileDir := fs.String("profile-dir", "", "profile YAML directory")
	dryRun := fs.Bool("dry-run", false, "print plans only")
	quiet := fs.Bool("quiet", false, "disable live progress output")
	progress := fs.Bool("progress", false, "force live progress output even when stderr is not a terminal (one plain line per tick, log-safe)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	entries, err := os.ReadDir(*dir)
	if err != nil {
		return fmt.Errorf("suite: read --configs directory: %w", err)
	}
	var plats []string
	if *platforms != "" {
		plats = strings.Split(*platforms, ",")
	}
	progW, progTTY := resolveProgress(*quiet, *progress)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		cfgPath := filepath.Join(*dir, e.Name())
		targets := plats
		if len(targets) == 0 {
			targets = []string{""}
		}
		for _, p := range targets {
			cfg, err := harness.LoadRunConfig(cfgPath)
			if err != nil {
				return err
			}
			if p != "" {
				cfg.Platform = p
			}
			if *profile != "" {
				cfg.Profile = *profile
			}
			fmt.Printf("=== %s  [%s]\n", e.Name(), cfg.Platform)
			opt := harness.Options{ProfileDir: *profileDir, DryRun: *dryRun, Progress: progW, ProgressTTY: progTTY}
			rr, err := (harness.Engine{}).Run(ctx, cfg, opt)
			if !*dryRun {
				updateRunbook(cfg.Metrics.OutputDir)
			}
			if err != nil {
				return fmt.Errorf("%s [%s]: %w", e.Name(), cfg.Platform, err)
			}
			if !*dryRun {
				generateMonitoringReport(cfg, rr)
			}
			if n := len(rr.Manifest.ContainerFailures); n > 0 {
				slog.Warn("platform container(s) failed during this suite run; its results have no headline",
					"config", e.Name(), "platform", cfg.Platform, "failures", n, "dir", rr.OutDir)
			}
			if ctx.Err() != nil {
				return fmt.Errorf("suite interrupted after %s [%s]; remaining configs not run", e.Name(), cfg.Platform)
			}
		}
	}
	return nil
}

// updateRunbook rebuilds <results root>/index.html so the run just finished has
// its page. Never fatal: the run's own results are already on disk.
func updateRunbook(root string) {
	if harness.IsRunbookDisabled() || root == "" {
		return
	}
	out := filepath.Join(root, harness.RunbookFile)
	if err := harness.BuildRunbook(root, out); err != nil {
		slog.Warn("run book not updated (the run's results are unaffected)", "out", out, "err", err)
		return
	}
	fmt.Println("run book:", out)
}

func cmdRunbook(args []string) error {
	fs := flag.NewFlagSet("runbook", flag.ExitOnError)
	dir := fs.String("results-dir", "results", "directory of run outputs")
	out := fs.String("output", "", "HTML file to write (default <results-dir>/"+harness.RunbookFile+")")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(*dir, harness.RunbookFile)
	}
	if err := harness.BuildRunbook(*dir, *out); err != nil {
		return fmt.Errorf("runbook: %w", err)
	}
	fmt.Println("wrote", *out)
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("results-dir", "results", "directory of run outputs")
	out := fs.String("output", "report.html", "HTML file to write")
	since := fs.String("since", "", "only runs started after this: a date (2006-01-02), RFC3339 time, or a duration ago (e.g. 36h)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	var opt harness.ReportOptions
	if *since != "" {
		t, err := parseSince(*since, time.Now())
		if err != nil {
			return err
		}
		opt.Since = t
	}
	if err := harness.BuildReport(*dir, *out, opt); err != nil {
		if !harness.IsIncompleteReport(err) {
			return fmt.Errorf("report: %w", err)
		}
		slog.Warn(err.Error())
	}
	fmt.Println("wrote", *out)
	return nil
}

// cmdDeploy shells out to deploy/docker/<platform>/<action>.sh so deployment
// logic stays in shell/compose, not Go. action is "up" or "down".
func cmdDeploy(ctx context.Context, args []string, action string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	platform := fs.String("platform", "", "platform name (required)")
	profile := fs.String("profile", "local", "resource profile")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	dirs := deployDirs()
	if *platform == "" {
		return fmt.Errorf("%w: --platform is required (one of: %s)", errUsage, strings.Join(dirs, ", "))
	}
	if strings.ContainsAny(*platform, `/\`) || strings.HasPrefix(*platform, ".") {
		return fmt.Errorf("%w: --platform %q must be a plain name (one of: %s)", errUsage, *platform, strings.Join(dirs, ", "))
	}
	script := filepath.Join("deploy", "docker", *platform, action+".sh")
	if _, err := os.Stat(script); err != nil {
		if len(dirs) == 0 {
			return fmt.Errorf("no deploy script %s: deploy/docker not found - run benchrunner from the repository root", script)
		}
		return fmt.Errorf("no deploy script %s (platforms with deploy scripts: %s)", script, strings.Join(dirs, ", "))
	}
	slog.Info("running deploy script", "script", script, "profile", *profile)
	cmd := exec.CommandContext(ctx, "bash", script, *profile)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Env = append(os.Environ(), "BENCH_PROFILE="+*profile)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s failed with exit code %d; its own output above says why (for a full capture: scripts/capture.sh <dir>)", script, ee.ExitCode())
		}
		return fmt.Errorf("%s: %w", script, err)
	}
	return nil
}

// deployDirs lists deploy/docker/<name> directories that have an up.sh.
func deployDirs() []string {
	m, _ := filepath.Glob(filepath.Join("deploy", "docker", "*", "up.sh"))
	out := make([]string, 0, len(m))
	for _, p := range m {
		out = append(out, filepath.Base(filepath.Dir(p)))
	}
	return out
}

// parseSince accepts a date, an RFC3339 timestamp, or a duration measured back
// from now, so a campaign's report can exclude older runs.
func parseSince(v string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: want a date (2006-01-02), RFC3339 time, or duration (36h)", v)
}

// benchrunner is the CLI entry point for the unified blockchain benchmark harness.
//
//	benchrunner run      --config configs/normalized/quick-smoke.yaml [--platform X] [--dry-run]
//	benchrunner suite    --configs configs/ --platforms a,b,c [--profile local]
//	benchrunner report   --results-dir ./results --output ./report.html
//	benchrunner setup    --platform fabric-cft --profile local
//	benchrunner teardown --platform fabric-cft
//	benchrunner list
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/harness"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "suite":
		err = cmdSuite(ctx, os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "setup":
		err = cmdDeploy(ctx, os.Args[2:], "up")
	case "teardown":
		err = cmdDeploy(ctx, os.Args[2:], "down")
	case "list":
		fmt.Println("registered adapters:", strings.Join(adapters.Registered(), ", "))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`benchrunner - unified blockchain benchmark harness

commands:
  run       run a single benchmark from a YAML config
  suite     run every config in a directory across one or more platforms
  report    build an HTML comparison from a results directory
  setup     bring a platform's docker-compose topology up (for a profile)
  teardown  bring a platform's topology down
  list      list registered platform adapters
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
	_ = fs.Parse(args)
	if *cfgPath == "" {
		return fmt.Errorf("--config is required")
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

	rr, err := harness.Engine{}.Run(ctx, cfg, opt)
	if err != nil {
		return err
	}
	if !*dryRun {
		generateMonitoringReport(ctx, cfg, rr)
	}
	return nil
}

// generateMonitoringReport writes the per-run monitoring HTML report. Failures
// here are logged, never fatal - a run that succeeded must not be reported as
// failed just because Prometheus was unreachable or a chart didn't render.
func generateMonitoringReport(ctx context.Context, cfg *harness.RunConfig, rr *harness.RunResult) {
	if err := monitoring.GenerateReport(ctx, cfg, rr); err != nil {
		fmt.Fprintln(os.Stderr, "warning: monitoring report:", err)
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
	_ = fs.Parse(args)

	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
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
				return fmt.Errorf("%s: %w", cfgPath, err)
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
			if err != nil {
				return fmt.Errorf("%s [%s]: %w", e.Name(), cfg.Platform, err)
			}
			if !*dryRun {
				generateMonitoringReport(ctx, cfg, rr)
			}
		}
	}
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("results-dir", "results", "directory of run outputs")
	out := fs.String("output", "report.html", "HTML file to write")
	_ = fs.Parse(args)
	if err := harness.BuildReport(*dir, *out); err != nil {
		return err
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
	_ = fs.Parse(args)
	if *platform == "" {
		return fmt.Errorf("--platform is required")
	}
	script := filepath.Join("deploy", "docker", *platform, action+".sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("no deploy script %s: %w", script, err)
	}
	cmd := exec.CommandContext(ctx, "bash", script, *profile)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Env = append(os.Environ(), "BENCH_PROFILE="+*profile)
	return cmd.Run()
}

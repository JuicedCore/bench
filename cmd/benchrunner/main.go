// benchrunner is the CLI entry point for the unified blockchain benchmark harness.
//
//	benchrunner run      --config configs/quick-smoke.yaml [--platform X] [--dry-run]
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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/harness"

	// Register adapters. fabricx/neuchain are Phase 3/4 skeletons that error on
	// Setup; they register so `list` and `suite --dry-run` see all five.
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

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to run config YAML (required)")
	platform := fs.String("platform", "", "override config.platform")
	profile := fs.String("profile", "", "override config.profile")
	profileDir := fs.String("profile-dir", "", "directory holding profile YAMLs (default deploy/profiles)")
	dryRun := fs.Bool("dry-run", false, "print the phase plan without generating load")
	caveat := fs.String("caveat", "", "append a caveat string to the manifest")
	generators := fs.Int("generators", 0, "concurrent load-generator instances sharing the adapter (default 1; use >1 for high-ceiling platforms)")
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

	opt := harness.Options{ProfileDir: *profileDir, DryRun: *dryRun, Generators: *generators}
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

	_, err = harness.Engine{}.Run(ctx, cfg, opt)
	return err
}

func cmdSuite(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("suite", flag.ExitOnError)
	dir := fs.String("configs", "configs", "directory of run config YAMLs")
	platforms := fs.String("platforms", "", "comma-separated platform list (default: each config's own)")
	profile := fs.String("profile", "", "override profile for all runs")
	profileDir := fs.String("profile-dir", "", "profile YAML directory")
	dryRun := fs.Bool("dry-run", false, "print plans only")
	_ = fs.Parse(args)

	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	var plats []string
	if *platforms != "" {
		plats = strings.Split(*platforms, ",")
	}

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
			opt := harness.Options{ProfileDir: *profileDir, DryRun: *dryRun}
			if _, err := (harness.Engine{}).Run(ctx, cfg, opt); err != nil {
				return fmt.Errorf("%s [%s]: %w", e.Name(), cfg.Platform, err)
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

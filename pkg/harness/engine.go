package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
	"github.com/juicedcore/bench/pkg/logx"
	"github.com/juicedcore/bench/pkg/metrics"
	"github.com/juicedcore/bench/pkg/workloads"
)

// PhaseResult is the aggregate for one load phase.
type PhaseResult struct {
	Name       string         `json:"name"`
	OfferedTPS int            `json:"offered_tps"` // nominal target for the phase
	Window     WindowInfo     `json:"window"`
	Result     metrics.Result `json:"result"`
	// Verdict, for sweep steps only: "held", or the rule that rejected the step.
	// Makes a sweep readable without re-deriving the saturation rules. A phase
	// cut short by Ctrl-C or a generator error is marked "interrupted" instead.
	Verdict string `json:"verdict,omitempty"`
}

// WindowInfo records the measurement window bounds relative to phase start.
type WindowInfo struct {
	PhaseStart time.Time `json:"phase_start"`
	WinStart   time.Time `json:"win_start"`
	WinEnd     time.Time `json:"win_end"`
}

// RunResult is the full output of one benchmark run.
type RunResult struct {
	Manifest      Manifest               `json:"manifest"`
	Phases        []PhaseResult          `json:"phases"`
	Headline      *metrics.Result        `json:"headline"` // hold phase for sweeps, the single phase otherwise
	SaturationTPS int                    `json:"saturation_tps,omitempty"`
	NativeScrapes []metrics.NativeScrape `json:"native_scrapes,omitempty"`
	SystemSamples []metrics.SystemSample `json:"system_samples,omitempty"`

	// OutDir is the directory results were written to (not itself part of the
	// JSON record on disk - a file doesn't need to know its own path - but set
	// on the in-memory RunResult so callers like report generation don't have to
	// recompute the timestamped path).
	OutDir string `json:"-"`
}

// Options tunes engine behaviour not expressed in the run config.
type Options struct {
	// ProfileDir overrides deploy/profiles.
	ProfileDir string
	// DryRun builds everything and prints the plan without submitting load.
	DryRun bool
	// Caveats are appended to the manifest (e.g. resource-starvation notes).
	Caveats []string
	// Generators is the number of concurrent load-generator instances sharing
	// the adapter + collector. Each gets its own workload (seed+i) and, in
	// open-loop, an equal share of the target rate; in closed-loop, an equal
	// share of the workers. Default 1. Use >1 for high-ceiling platforms
	// (Fabric-X, NeuChain) where one Go generator + one gRPC conn is the
	// bottleneck - size it to the profile's load_gen_cpus.
	Generators int
	// Progress, if non-nil, receives one live-updating line per second while
	// each phase runs (phase name, elapsed/total, offered/confirmed TPS, p99,
	// failure rate). nil disables it entirely - the run stays silent until it
	// writes results, as before.
	Progress io.Writer
	// ProgressTTY selects redraw-in-place (\r) rendering when true, or one
	// plain line per tick when false (log-file-safe: no control characters).
	ProgressTTY bool
}

// Engine executes runs.
type Engine struct{}

// Run performs one benchmark run end to end and writes results under
// cfg.Metrics.OutputDir/<platform>/<timestamp>/.
func (Engine) Run(ctx context.Context, cfg *RunConfig, opt Options) (*RunResult, error) {
	prof, err := LoadProfile(cfg.Profile, opt.ProfileDir)
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}
	topo, err := prof.Topo(cfg.Platform)
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}

	ad, err := adapters.New(cfg.Platform)
	if err != nil {
		return nil, err
	}
	log := slog.Default().With("platform", cfg.Platform, "run", cfg.Name)

	wl, err := workloads.New(cfg.Workload, workloads.Config{
		KeySpace:        cfg.Load.KeySpace,
		KeyDistribution: cfg.Load.KeyDistribution,
		ZipfianConstant: cfg.Load.ZipfianConstant,
		ReadWriteRatio:  cfg.Load.ReadWriteRatio,
		ValueSizeBytes:  cfg.Load.ValueSizeBytes,
		Seed:            cfg.Load.Seed,
	})
	if err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}

	acfg := adapters.AdapterConfig{
		Profile:    cfg.Profile,
		Workload:   cfg.Workload,
		Normalized: cfg.Normalized,
		Extra:      cfg.Adapter,
		Logger:     log.With("component", "adapter"),
	}
	if cfg.Adapter != nil {
		if v, ok := cfg.Adapter["conn_profile"].(string); ok {
			acfg.ConnProfilePath = v
		}
	}

	started := time.Now()
	man := Manifest{
		RunName:          cfg.Name,
		Platform:         cfg.Platform,
		Workload:         cfg.Workload,
		Profile:          cfg.Profile,
		Normalized:       cfg.Normalized,
		StartedAt:        started,
		HarnessGitSHA:    harnessGitSHA(),
		PlatformVersion:  "unknown",
		StateDB:          actualStateDB(topo.EffectiveStateDB(cfg.Normalized)),
		StateDBRequested: topo.EffectiveStateDB(cfg.Normalized),
		OrdererBatch:     topo.OrdererBatch,
		Nodes:            topo.Nodes,
		Seed:             cfg.Load.Seed,
		KeySpace:         cfg.Load.KeySpace,
		KeyDistribution:  cfg.Load.KeyDistribution,
		ZipfianConstant:  cfg.Load.ZipfianConstant,
		ReadWriteRatio:   cfg.Load.ReadWriteRatio,
		ValueSizeBytes:   cfg.Load.ValueSizeBytes,
		WarmupSec:        cfg.Metrics.Warmup.D().Seconds(),
		CooldownSec:      cfg.Metrics.Cooldown.D().Seconds(),
		Caveats:          opt.Caveats,
	}
	if vp, ok := ad.(adapters.VersionReporter); ok {
		if v := vp.PlatformVersion(); v != "" {
			man.PlatformVersion = v
		}
	}
	// Deploy scripts export the concrete image/tag; prefer it when set.
	if v := os.Getenv("BENCH_PLATFORM_VERSION"); v != "" {
		man.PlatformVersion = v
	}
	if cp, ok := ad.(adapters.CryptoReporter); ok {
		man.Crypto = cp.CryptoInfo()
	}
	applyResourceEnv(&man)
	// A platform that could not honour the requested state DB is not holding
	// that fairness lever, so say so next to the numbers rather than only in the
	// manifest fields.
	if man.StateDB != man.StateDBRequested {
		man.Caveats = append(man.Caveats, fmt.Sprintf(
			"state-db parity not held: run requested %s, platform actually ran %s",
			man.StateDBRequested, man.StateDB))
	}

	outDir := filepath.Join(cfg.Metrics.OutputDir, cfg.Platform, started.Format("20060102-150405"))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create result dir (metrics.output_dir=%s): %w", cfg.Metrics.OutputDir, err)
	}
	// Everything logged from here on is also kept in <outDir>/run.log, at debug
	// level, so a failed run can be diagnosed from its result directory alone.
	if l, closeLog, lerr := logx.WithFile(log, filepath.Join(outDir, "run.log")); lerr != nil {
		log.Warn("run log unavailable; diagnostics go to stderr only", "err", lerr)
	} else {
		log = l
		acfg.Logger = log.With("component", "adapter")
		defer func() { _ = closeLog() }()
	}
	log.Info("run starting", "workload", cfg.Workload, "profile", cfg.Profile, "normalized", cfg.Normalized, "out_dir", outDir)
	for _, c := range man.Caveats {
		log.Warn("caveat", "text", c)
	}

	if opt.DryRun {
		plan := buildPhases(cfg)
		fmt.Printf("DRY RUN %s/%s (%s)\n", cfg.Platform, cfg.Workload, cfg.Name)
		for _, ph := range plan {
			fmt.Printf("  phase %-10s target=%-7d dur=%s mode=%s\n", ph.name, ph.profile.TargetTPS, ph.profile.Duration, ph.profile.Mode)
		}
		man.EndedAt = time.Now()
		if err := man.Write(filepath.Join(outDir, "manifest.json")); err != nil {
			return nil, fmt.Errorf("dry run: write manifest: %w", err)
		}
		return &RunResult{Manifest: man, OutDir: outDir}, nil
	}

	teardown := func() {
		tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ad.Teardown(tctx); err != nil {
			log.Warn("adapter teardown failed (client resources may have leaked; the network itself is untouched)", "err", err)
		}
	}
	log.Info("adapter setup")
	if err := ad.Setup(ctx, acfg); err != nil {
		// Leave a reason behind: outDir already exists and an aborted run
		// would otherwise leave an empty result dir with no clue why.
		msg := fmt.Sprintf("adapter setup failed for platform %s (profile %s)\n\n%v\n\n%s", cfg.Platform, cfg.Profile, err, setupHint)
		if werr := os.WriteFile(filepath.Join(outDir, "error.txt"), []byte(msg), 0o644); werr != nil {
			log.Warn("could not write error.txt", "err", werr)
		}
		log.Error("adapter setup failed", "err", err, "error_file", filepath.Join(outDir, "error.txt"))
		// The interface allows Teardown after a partial Setup; release whatever
		// connections were opened before the failure.
		teardown()
		return nil, fmt.Errorf("adapter setup (%s): %w", cfg.Platform, err)
	}
	defer teardown()

	collector := metrics.NewCollector()

	ngen := generatorCount(opt.Generators, prof, topo, cfg.Normalized)
	man.Generators = ngen
	if opt.Generators > 0 && cfg.Normalized && opt.Generators != generatorCount(0, prof, topo, true) {
		man.Caveats = append(man.Caveats, fmt.Sprintf(
			"generator count overridden to %d: each generator uses seed+i, so the key-access sequence differs from runs using the profile default",
			ngen))
	}
	// One generator + its own workload per instance. Generator 0 reuses wl (so
	// single-generator runs are byte-identical to before).
	gens := make([]*loadgen.Generator, ngen)
	gens[0] = &loadgen.Generator{ID: 0, Adapter: ad, Source: wl, Collector: collector}
	for i := 1; i < ngen; i++ {
		wi, werr := workloads.New(cfg.Workload, workloads.Config{
			KeySpace:        cfg.Load.KeySpace,
			KeyDistribution: cfg.Load.KeyDistribution,
			ZipfianConstant: cfg.Load.ZipfianConstant,
			ReadWriteRatio:  cfg.Load.ReadWriteRatio,
			ValueSizeBytes:  cfg.Load.ValueSizeBytes,
			Seed:            cfg.Load.Seed + int64(i),
		})
		if werr != nil {
			return nil, fmt.Errorf("workload for generator %d: %w", i, werr)
		}
		gens[i] = &loadgen.Generator{ID: i, Adapter: ad, Source: wi, Collector: collector}
	}

	// System sampling for the whole run.
	var sampler *metrics.SystemSampler
	sampCtx, sampCancel := context.WithCancel(ctx)
	if cfg.System.Enabled {
		sampler = &metrics.SystemSampler{
			Interval:     cfg.System.SampleInterval.D(),
			NamePrefixes: cfg.System.ContainerNames,
			Logger:       log.With("component", "sampler"),
		}
		go sampler.Run(sampCtx)
	}

	phases := buildPhases(cfg)
	rr := &RunResult{Manifest: man, OutDir: outDir}
	// caveat records a disclosure next to the numbers and says it out loud now,
	// so an operator watching the run does not have to open the manifest to learn
	// something went wrong.
	caveat := func(format string, a ...any) {
		c := fmt.Sprintf(format, a...)
		rr.Manifest.Caveats = append(rr.Manifest.Caveats, c)
		log.Warn("caveat", "text", c)
	}
	if sampler == nil {
		caveat("system_metrics disabled: no resource trace, and a platform container that dies mid-run will NOT be detected")
	}
	warm := cfg.Metrics.Warmup.D()
	cool := cfg.Metrics.Cooldown.D()

	// Sweep bookkeeping. bestPassing is the highest sweep step that stayed under
	// the failure threshold; the hold phase is retargeted to hold_fraction of it
	// so the headline is measured just below the MEASURED knee rather than just
	// below the top of the configured ladder. consecFailed drives early abort.
	sweep := cfg.Load.Sweep
	abortAfter := 0
	if sweep.AbortAfterFailedSteps != nil {
		abortAfter = *sweep.AbortAfterFailedSteps
	}
	bestPassing, consecFailed := 0, 0

	for i, ph := range phases {
		isSweepStep := strings.HasPrefix(ph.name, "sweep-")

		// Early abort: once the platform has failed abort_after_failed_steps
		// steps in a row it will not recover further up the ladder, so stop
		// offering steps and go straight to hold. probe and hold never abort.
		if isSweepStep && abortAfter > 0 && consecFailed >= abortAfter {
			rr.Manifest.SkippedSteps = append(rr.Manifest.SkippedSteps, ph.profile.TargetTPS)
			continue
		}
		if ph.name == "hold" && sweep.Enabled {
			ph.profile.TargetTPS = holdTarget(bestPassing, sweep)
		}

		phaseStart := time.Now()
		log.Info("phase start", "phase", ph.name, "offered_tps", ph.profile.TargetTPS, "workers", ph.profile.Workers, "duration", ph.profile.Duration)
		var liveProg *progressReporter
		if opt.Progress != nil {
			liveProg = newProgressReporter(opt.Progress, opt.ProgressTTY)
			liveProg.start(ph.name, ph.profile.TargetTPS, ph.profile.Duration, phaseStart, collector)
		}
		genErr := runGenerators(ctx, gens, splitProfile(ph.profile, len(gens)))
		if liveProg != nil {
			liveProg.stop()
		}
		interrupted := ctx.Err() != nil
		if genErr != nil && !interrupted {
			log.Error("load generator failed", "phase", ph.name, "err", genErr)
		}

		// The window is cut from the offered-load schedule, not from when the
		// generators returned: they also drain in-flight finality waits, which on
		// a platform that stops committing adds up to finality_wait. Ending the
		// window at the drain instead slid it past the last scheduled send, so a
		// 30s probe on such a platform measured 2 transactions.
		loadEnd := phaseStart.Add(ph.profile.Duration)
		if now := time.Now(); now.Before(loadEnd) {
			loadEnd = now
		}
		w := metrics.Window{Start: phaseStart.Add(warm), End: loadEnd.Add(-cool)}
		if ph.noWindowTrim {
			w = metrics.Window{Start: phaseStart, End: loadEnd}
		}
		headlinePhase := ph.name == "hold" || len(phases) == 1
		if !w.End.After(w.Start) {
			// Phase shorter than warmup+cooldown: measure the whole thing. The
			// sweep probe is routinely this short; for a headline phase it means
			// the headline includes warmup, which must be disclosed.
			w = metrics.Window{Start: phaseStart, End: loadEnd}
			if headlinePhase && !interrupted {
				caveat("phase %s ran %s, not longer than warmup+cooldown (%s+%s): warmup and cooldown were NOT trimmed from the headline",
					ph.name, loadEnd.Sub(phaseStart).Round(time.Second), warm, cool)
			} else {
				log.Debug("phase shorter than warmup+cooldown; measuring whole phase", "phase", ph.name)
			}
		}
		res := collector.Aggregate(w)
		pr := PhaseResult{
			Name:       ph.name,
			OfferedTPS: ph.profile.TargetTPS,
			Window:     WindowInfo{PhaseStart: phaseStart, WinStart: w.Start, WinEnd: w.End},
			Result:     res,
		}
		rr.Phases = append(rr.Phases, pr)
		log.Info("phase end", "phase", ph.name, "submitted", res.Submitted, "committed", res.Committed,
			"confirmed_tps", fmt.Sprintf("%.1f", res.ConfirmedTPS), "fail_rate", fmt.Sprintf("%.4f", res.FailureRate),
			"e2e_p99_ms", fmt.Sprintf("%.2f", pctl(res.E2E, "p99")))
		for _, e := range res.Errors {
			log.Warn("phase errors", "phase", ph.name, "count", e.Count, "message", e.Message)
		}

		// A phase cut short - Ctrl-C/SIGTERM, or a generator that returned an
		// error - measured part of its schedule. Keep it, marked, never as a
		// headline, and do not start the remaining phases.
		if interrupted || genErr != nil {
			rr.Phases[len(rr.Phases)-1].Verdict = "interrupted"
			rest := make([]string, 0, len(phases)-i-1)
			for _, r := range phases[i+1:] {
				rest = append(rest, r.name)
			}
			reason := "run interrupted (signal or cancelled context)"
			if !interrupted {
				reason = fmt.Sprintf("load generator error: %v", genErr)
			}
			caveat("%s during phase %s after %s: that phase is partial and not a headline; phases never run: %s",
				reason, ph.name, time.Since(phaseStart).Round(time.Second), orDefault(strings.Join(rest, ", "), "none"))
			rr.Headline = nil
			break
		}

		if isSweepStep {
			v := stepVerdict(ph.profile.TargetTPS, res, sweep)
			rr.Phases[len(rr.Phases)-1].Verdict = orDefault(v, "held")
			if v == "" {
				consecFailed = 0
				if ph.profile.TargetTPS > bestPassing {
					bestPassing = ph.profile.TargetTPS
				}
			} else {
				consecFailed++
			}
		}

		if headlinePhase {
			cp := res
			rr.Headline = &cp
		}

		// A platform container that failed mid-run (typically OOM-killed at its
		// memory limit) leaves a broken network: every later phase would measure
		// the failure, not the platform, and the hold phase would headline 0 TPS.
		// Stop here, keep what was measured before, and say what died.
		if failures := sampler.Failures(); len(failures) > 0 {
			rr.Manifest.ContainerFailures = failures
			var names []string
			for _, e := range failures {
				caveat("platform container %s", e.String())
				// Grab the log now, before teardown removes the container and
				// the cause with it. Use Background: ctx may already be cancelled.
				logPath := filepath.Join(outDir, "container-logs", e.Name+".log")
				if err := metrics.CaptureLogs(context.Background(), e.Name, logPath, 5000); err != nil {
					caveat("could not capture logs of %s: %v", e.Name, err)
				} else {
					log.Info("captured container log", "container", e.Name, "path", logPath)
				}
			}
			for _, rest := range phases[i+1:] {
				names = append(names, rest.name)
			}
			if len(names) > 0 {
				caveat("run stopped during %s because a platform container failed; phases never run: %s",
					ph.name, strings.Join(names, ", "))
			}
			// The phase the container died in measured a half-dead network
			// and cannot be a headline either.
			rr.Headline = nil
			break
		}
	}

	// Determine saturation: highest sweep step whose failure rate stayed under threshold.
	if cfg.Load.Sweep.Enabled {
		rr.SaturationTPS = detectSaturation(rr.Phases, cfg.Load.Sweep)
		// Every step holding means the ladder ran out before the platform did: the
		// knee is somewhere above the top step, and reporting the top step as
		// "saturation" would understate the platform.
		if n := len(sweep.Steps); n > 0 && len(rr.Manifest.SkippedSteps) == 0 && rr.SaturationTPS == sweep.Steps[n-1] {
			caveat("every sweep step held: the platform did not saturate within the ladder, so %d TPS is a lower bound on its knee, not the knee",
				rr.SaturationTPS)
		}
		if rr.SaturationTPS == 0 && len(rr.Phases) > 0 {
			caveat("no sweep step held (failure <= %.1f%%, goodput >= %.0f%%, send-gap p99 <= %.0f ms); hold ran at the probe rate (%d TPS) and the headline is a floor, not a saturation figure",
				sweep.MaxFailRate*100, sweep.GoodputRatio*100, sweep.MaxSendGapMs, sweep.ProbeTPS)
		}
		if n := len(rr.Manifest.SkippedSteps); n > 0 {
			caveat("sweep aborted early after %d consecutive failed steps; %d higher step(s) were never offered",
				abortAfter, n)
		}
	}

	sampCancel()
	if sampler != nil {
		rr.SystemSamples = sampler.Samples()
		if reason := sampler.Unhealthy(); reason != "" {
			caveat("container sampling was degraded: %s - resource numbers are incomplete and a dead platform container may have gone undetected", reason)
		}
	}
	if n := collector.ClampedLatencies(); n > 0 {
		caveat("%d latency observations were out of range (negative: T3 before T1/T2, usually adapter or clock timestamp error; or above 5 min) and were clamped", n)
	}

	// One native scrape at the end if the platform exposes an endpoint.
	if ep := ad.MetricsEndpoint(); ep != "" {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if ns, err := metrics.ScrapeNative(sctx, ep); err == nil {
			rr.NativeScrapes = append(rr.NativeScrapes, *ns)
		} else {
			caveat("native metrics scrape of %s failed: %v (informational metrics only; harness numbers unaffected)", ep, err)
		}
		cancel()
	}

	// rr.Manifest, not man: the phase loop appends skipped steps and caveats to
	// rr.Manifest, and reassigning from man here would discard them.
	rr.Manifest.EndedAt = time.Now()

	if err := writeResults(outDir, cfg, rr); err != nil {
		log.Error("writing results failed", "dir", outDir, "err", err)
		return rr, fmt.Errorf("write results to %s: %w", outDir, err)
	}
	warnFailedHeadline(log, rr)
	log.Info("run finished", "out_dir", outDir, "caveats", len(rr.Manifest.Caveats))
	fmt.Printf("results written to %s\n", outDir)
	return rr, nil
}

// setupHint is appended to error.txt when adapter setup fails: almost every
// setup failure is the network or its connection material, not the harness.
const setupHint = `What to check:
  1. Is the network up?            docker ps   (bring it up: benchrunner setup --platform <p> --profile <profile>)
  2. Was connection.env sourced?   set -a; source deploy/docker/<platform>/connection.env; set +a
     An unset BENCH_ADAPTER_* variable expands to an empty adapter key.
  3. Do the cert/key/TLS paths in connection.env exist and match the running network
     (a re-deployed network regenerates crypto material)?
  4. Rerun with --log-level debug and read run.log in this directory.
See docs/README.md#troubleshooting for the full error lookup.`

// warnFailedHeadline says loudly, at the end of a run, when the numbers just
// written are not a measurement. The exit code is unchanged; summary.txt and the
// comparison report already refuse such runs, but a campaign log should too.
func warnFailedHeadline(log *slog.Logger, rr *RunResult) {
	h := rr.Headline
	if h == nil {
		if len(rr.Phases) > 0 {
			log.Warn("run has no headline: it did not complete a headline phase on a healthy platform; see caveats in summary.txt", "dir", rr.OutDir)
		}
		return
	}
	var why []string
	if h.Committed == 0 {
		why = append(why, "headline phase committed nothing")
	}
	if !h.InvariantOK {
		why = append(why, "accounting invariant broken (submitted != committed+failed)")
	}
	if len(why) == 0 {
		return
	}
	args := []any{"reason", strings.Join(why, "; "), "summary", filepath.Join(rr.OutDir, "summary.txt")}
	for i, e := range h.Errors {
		if i == 3 {
			break
		}
		args = append(args, fmt.Sprintf("error_%d", i+1), fmt.Sprintf("%dx %s", e.Count, e.Message))
	}
	log.Warn("FAILED RUN - these results are not a throughput measurement", args...)
}

type phase struct {
	name         string
	profile      loadgen.LoadProfile
	noWindowTrim bool
}

// splitProfile divides a phase's load across n generators: TargetTPS / RampFrom
// (open-loop) and Workers (closed-loop) are split as evenly as possible;
// generator 0 takes any remainder.
func splitProfile(p loadgen.LoadProfile, n int) []loadgen.LoadProfile {
	if n <= 1 {
		return []loadgen.LoadProfile{p}
	}
	out := make([]loadgen.LoadProfile, n)
	share := func(total, i int) int {
		q := total / n
		if i == 0 {
			q += total % n
		}
		return q
	}
	for i := 0; i < n; i++ {
		pi := p
		pi.TargetTPS = share(p.TargetTPS, i)
		if p.RampFrom > 0 {
			pi.RampFrom = share(p.RampFrom, i)
		}
		if p.Workers > 0 {
			pi.Workers = share(p.Workers, i)
			if pi.Workers == 0 {
				pi.Workers = 1
			}
		}
		out[i] = pi
	}
	return out
}

// runGenerators runs every generator concurrently for one phase and waits for
// all of them. Errors from every generator are joined, each tagged with its index.
func runGenerators(ctx context.Context, gens []*loadgen.Generator, profiles []loadgen.LoadProfile) error {
	if len(gens) == 1 {
		return gens[0].Run(ctx, profiles[0])
	}
	errs := make([]error, len(gens))
	var wg sync.WaitGroup
	for i, g := range gens {
		wg.Add(1)
		go func(i int, g *loadgen.Generator, p loadgen.LoadProfile) {
			defer wg.Done()
			if err := g.Run(ctx, p); err != nil {
				errs[i] = fmt.Errorf("generator %d: %w", i, err)
			}
		}(i, g, profiles[i])
	}
	wg.Wait()
	return errors.Join(errs...)
}

func buildPhases(cfg *RunConfig) []phase {
	l := cfg.Load
	common := loadgen.LoadProfile{
		Mode:         loadgen.Mode(orDefault(l.Mode, "open-loop")),
		FinalityWait: l.FinalityWait.D(),
	}

	if l.Sweep.Enabled {
		var out []phase
		probe := common
		probe.TargetTPS = l.Sweep.ProbeTPS
		probe.Duration = l.Sweep.ProbeDur.D()
		out = append(out, phase{name: "probe", profile: probe})

		for _, s := range l.Sweep.Steps {
			p := common
			p.TargetTPS = s
			p.Duration = l.Sweep.StepDur.D()
			out = append(out, phase{name: fmt.Sprintf("sweep-%d", s), profile: p})
		}
		// The real hold target is only known once the sweep has run, so Run
		// rewrites this from the highest passing step before the phase starts
		// (see holdTarget). The top-step approximation here exists purely so
		// --dry-run can print an upper bound for the plan.
		hold := common
		top := 0
		if len(l.Sweep.Steps) > 0 {
			top = l.Sweep.Steps[len(l.Sweep.Steps)-1]
		}
		hold.TargetTPS = int(float64(top) * l.Sweep.HoldFrac)
		hold.Duration = l.Sweep.HoldDur.D()
		out = append(out, phase{name: "hold", profile: hold})
		return out
	}

	p := common
	p.Duration = l.HoldDur.D()
	if l.Mode == "closed-loop" {
		p.Workers = l.Workers
		if p.Duration == 0 {
			p.Duration = 60 * time.Second
		}
		return []phase{{name: "single", profile: p}}
	}
	// open-loop single phase, optional ramp
	p.TargetTPS = l.TargetTPS
	if l.RampTo > 0 {
		p.RampFrom = orInt(l.RampFrom, l.StartTPS, 1)
		p.TargetTPS = l.RampTo
		p.RampDur = l.RampDur.D()
		p.Duration = l.RampDur.D() + l.HoldDur.D()
	}
	if p.Duration == 0 {
		p.Duration = 60 * time.Second
	}
	return []phase{{name: "single", profile: p}}
}

// generatorCount decides how many load generators drive the run.
//
// An explicit --generators wins. Otherwise it is one generator per load-generator
// CPU the profile reserves. For a normalized run only the profile-wide budget is
// used, never a per-platform override: generator i draws keys with seed+i, so a
// different count means a different key-access sequence, which would quietly
// break the "one seed drives every platform" fairness lever.
func generatorCount(explicit int, prof *Profile, topo PlatformTopo, normalized bool) int {
	if explicit > 0 {
		return explicit
	}
	cpus := prof.Budget.LoadGenCPUs
	if !normalized && topo.LoadGenCPUs > 0 {
		cpus = topo.LoadGenCPUs
	}
	if n := int(cpus); n > 1 {
		return n
	}
	return 1
}

// applyResourceEnv records the resource budget the deploy script actually applied.
//
// The profile's per-platform figures are not used: platforms run different
// numbers of containers, so the fairness lever is the TOTAL budget, which
// deploy/docker/lib.sh apply_budget splits evenly across the containers that are
// really running and reports via connection.env. Without that report nothing
// proves the platform was constrained at all, and the run says so.
func applyResourceEnv(man *Manifest) {
	envF := func(k string) float64 {
		raw := strings.TrimSpace(os.Getenv(k))
		if raw == "" {
			return 0
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			man.Caveats = append(man.Caveats, fmt.Sprintf("%s=%q from connection.env is not a number; recorded as 0", k, raw))
		}
		return v
	}
	rawN := strings.TrimSpace(os.Getenv("BENCH_RESOURCE_CONTAINERS"))
	n, nerr := strconv.Atoi(rawN)
	if rawN != "" && nerr != nil {
		man.Caveats = append(man.Caveats, fmt.Sprintf(
			"BENCH_RESOURCE_CONTAINERS=%q from connection.env is not an integer: container limits are unverified, so this run's hardware share is unknown", rawN))
		return
	}
	if n <= 0 {
		man.Caveats = append(man.Caveats,
			"resource budget not reported by the deploy script (BENCH_RESOURCE_CONTAINERS unset - was connection.env sourced?): container limits are unverified, so this run's hardware share is unknown")
		return
	}
	man.ResourceContainers = n
	man.ResourceLimit = Limits{CPUs: envF("BENCH_RESOURCE_CPUS_EACH"), Memory: strings.TrimSpace(os.Getenv("BENCH_RESOURCE_MEMORY_EACH"))}
	if split := strings.TrimSpace(os.Getenv("BENCH_RESOURCE_MEMORY_SPLIT")); split != "" {
		man.ResourceMemory = map[string]string{}
		for _, kv := range strings.Split(split, ",") {
			if name, mem, ok := strings.Cut(kv, "="); ok {
				man.ResourceMemory[name] = mem
			}
		}
		man.ResourceMemoryWeights = strings.Trim(strings.TrimSpace(os.Getenv("BENCH_RESOURCE_MEMORY_WEIGHTS")), `"`)
	}
	man.ResourceCPUsTotal = envF("BENCH_RESOURCE_CPUS_TOTAL")
	man.ResourceMemTotalGB = envF("BENCH_RESOURCE_MEMORY_TOTAL_GB")
}

// actualStateDB reports the world-state backend the platform really came up on.
// Deploy scripts export BENCH_ACTUAL_STATE_DB from connection.env because the
// requested value can be unachievable - Drunix's up.sh always deploys YugabyteDB
// even for a normalized run. Absent the variable, assume the request was honoured.
func actualStateDB(requested string) string {
	if v := strings.TrimSpace(os.Getenv("BENCH_ACTUAL_STATE_DB")); v != "" {
		return v
	}
	return requested
}

// stepVerdict is the single definition of "this sweep step held". Both the live
// phase loop and detectSaturation go through it, so the knee can never be judged
// by two different rules. It returns "" when the step held, otherwise the rule
// that rejected it.
//
// Three rules, because the obvious one is not enough. Failure rate alone missed
// every real saturation on record: the historical fabric-cft sweep confirmed
// 1000, 1753, 618 and then 0 TPS at offered 1000, 2000, 5000 and 10000 with a
// failure rate of 0.0000 throughout, so it "held" at 10000 and headlined a rate
// ten times its real knee. A platform past its knee commits less rather than
// failing more, so the step is also required to deliver its offered goodput and
// to have been sent on schedule.
func stepVerdict(offeredTPS int, r metrics.Result, s SweepConfig) string {
	switch {
	case r.Submitted == 0:
		// Nothing scheduled in the window reached the collector. Every other rule
		// passes vacuously on an empty window (0% failures), so this must come first.
		return "no transactions in the measurement window"
	case r.FailureRate > s.MaxFailRate:
		return fmt.Sprintf("failure rate %.2f%% > %.2f%%", r.FailureRate*100, s.MaxFailRate*100)
	case offeredTPS > 0 && r.ConfirmedTPS < s.GoodputRatio*float64(offeredTPS):
		return fmt.Sprintf("goodput %.0f of %d TPS offered (< %.0f%%)", r.ConfirmedTPS, offeredTPS, s.GoodputRatio*100)
	case r.SendGap.Percentiles["p99"] > s.MaxSendGapMs:
		return fmt.Sprintf("send-gap p99 %.0f ms > %.0f ms (generator fell behind)", r.SendGap.Percentiles["p99"], s.MaxSendGapMs)
	}
	return ""
}

// holdTarget is the offered rate for the hold phase: hold_fraction of the highest
// step that actually held. With no passing step there is no knee to sit below, so
// fall back to the probe rate - a floor reading, caveated by the caller - rather
// than holding at a rate the platform already demonstrably cannot serve.
func holdTarget(bestPassing int, s SweepConfig) int {
	if bestPassing <= 0 {
		return s.ProbeTPS
	}
	if t := int(float64(bestPassing) * s.HoldFrac); t > 0 {
		return t
	}
	return 1
}

func detectSaturation(phases []PhaseResult, s SweepConfig) int {
	best := 0
	for _, ph := range phases {
		if !strings.HasPrefix(ph.Name, "sweep-") {
			continue
		}
		if stepVerdict(ph.OfferedTPS, ph.Result, s) == "" && ph.OfferedTPS > best {
			best = ph.OfferedTPS
		}
	}
	return best
}

func writeResults(dir string, cfg *RunConfig, rr *RunResult) error {
	if err := rr.Manifest.Write(filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644); err != nil {
		return err
	}
	if err := writeSummaryText(filepath.Join(dir, "summary.txt"), cfg, rr); err != nil {
		return err
	}
	if cfg.Metrics.OutputFormat == "csv" {
		return writePhaseCSV(filepath.Join(dir, "phases.csv"), rr)
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

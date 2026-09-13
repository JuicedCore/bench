package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
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
	// Makes a sweep readable without re-deriving the saturation rules.
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
		return nil, err
	}

	ad, err := adapters.New(cfg.Platform)
	if err != nil {
		return nil, err
	}

	wl, err := workloads.New(cfg.Workload, workloads.Config{
		KeySpace:        cfg.Load.KeySpace,
		KeyDistribution: cfg.Load.KeyDistribution,
		ZipfianConstant: cfg.Load.ZipfianConstant,
		ReadWriteRatio:  cfg.Load.ReadWriteRatio,
		ValueSizeBytes:  cfg.Load.ValueSizeBytes,
		Seed:            cfg.Load.Seed,
	})
	if err != nil {
		return nil, err
	}

	acfg := adapters.AdapterConfig{
		Profile:    cfg.Profile,
		Workload:   cfg.Workload,
		Normalized: cfg.Normalized,
		Extra:      cfg.Adapter,
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
		return nil, err
	}

	if opt.DryRun {
		plan := buildPhases(cfg)
		fmt.Printf("DRY RUN %s/%s (%s)\n", cfg.Platform, cfg.Workload, cfg.Name)
		for _, ph := range plan {
			fmt.Printf("  phase %-10s target=%-7d dur=%s mode=%s\n", ph.name, ph.profile.TargetTPS, ph.profile.Duration, ph.profile.Mode)
		}
		man.EndedAt = time.Now()
		_ = man.Write(filepath.Join(outDir, "manifest.json"))
		return &RunResult{Manifest: man, OutDir: outDir}, nil
	}

	if err := ad.Setup(ctx, acfg); err != nil {
		return nil, fmt.Errorf("adapter setup: %w", err)
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = ad.Teardown(tctx)
	}()

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
	gens[0] = &loadgen.Generator{Adapter: ad, Source: wl, Collector: collector}
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
			return nil, werr
		}
		gens[i] = &loadgen.Generator{Adapter: ad, Source: wi, Collector: collector}
	}

	// System sampling for the whole run.
	var sampler *metrics.SystemSampler
	sampCtx, sampCancel := context.WithCancel(ctx)
	if cfg.System.Enabled {
		sampler = &metrics.SystemSampler{
			Interval:     cfg.System.SampleInterval.D(),
			NamePrefixes: cfg.System.ContainerNames,
		}
		go sampler.Run(sampCtx)
	}

	phases := buildPhases(cfg)
	rr := &RunResult{Manifest: man, OutDir: outDir}
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
		var liveProg *progressReporter
		if opt.Progress != nil {
			liveProg = newProgressReporter(opt.Progress, opt.ProgressTTY)
			liveProg.start(ph.name, ph.profile.TargetTPS, ph.profile.Duration, phaseStart, collector)
		}
		genErr := runGenerators(ctx, gens, splitProfile(ph.profile, len(gens)))
		if liveProg != nil {
			liveProg.stop()
		}
		if genErr != nil && ctx.Err() != nil {
			break
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
		if !w.End.After(w.Start) {
			// phase shorter than warmup+cooldown: measure the whole thing
			w = metrics.Window{Start: phaseStart, End: loadEnd}
		}
		res := collector.Aggregate(w)
		pr := PhaseResult{
			Name:       ph.name,
			OfferedTPS: ph.profile.TargetTPS,
			Window:     WindowInfo{PhaseStart: phaseStart, WinStart: w.Start, WinEnd: w.End},
			Result:     res,
		}
		rr.Phases = append(rr.Phases, pr)

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

		if ph.name == "hold" || len(phases) == 1 {
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
				rr.Manifest.Caveats = append(rr.Manifest.Caveats, "platform container "+e.String())
			}
			for _, rest := range phases[i+1:] {
				names = append(names, rest.name)
			}
			if len(names) > 0 {
				rr.Manifest.Caveats = append(rr.Manifest.Caveats, fmt.Sprintf(
					"run stopped during %s because a platform container failed; phases never run: %s",
					ph.name, strings.Join(names, ", ")))
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
			rr.Manifest.Caveats = append(rr.Manifest.Caveats, fmt.Sprintf(
				"every sweep step held: the platform did not saturate within the ladder, so %d TPS is a lower bound on its knee, not the knee",
				rr.SaturationTPS))
		}
		if rr.SaturationTPS == 0 {
			rr.Manifest.Caveats = append(rr.Manifest.Caveats, fmt.Sprintf(
				"no sweep step held (failure <= %.1f%%, goodput >= %.0f%%, send-gap p99 <= %.0f ms); hold ran at the probe rate (%d TPS) and the headline is a floor, not a saturation figure",
				sweep.MaxFailRate*100, sweep.GoodputRatio*100, sweep.MaxSendGapMs, sweep.ProbeTPS))
		}
		if n := len(rr.Manifest.SkippedSteps); n > 0 {
			rr.Manifest.Caveats = append(rr.Manifest.Caveats, fmt.Sprintf(
				"sweep aborted early after %d consecutive failed steps; %d higher step(s) were never offered",
				abortAfter, n))
		}
	}

	sampCancel()
	if sampler != nil {
		rr.SystemSamples = sampler.Samples()
	}

	// One native scrape at the end if the platform exposes an endpoint.
	if ep := ad.MetricsEndpoint(); ep != "" {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if ns, err := metrics.ScrapeNative(sctx, ep); err == nil {
			rr.NativeScrapes = append(rr.NativeScrapes, *ns)
		}
		cancel()
	}

	// rr.Manifest, not man: the phase loop appends skipped steps and caveats to
	// rr.Manifest, and reassigning from man here would discard them.
	rr.Manifest.EndedAt = time.Now()

	if err := writeResults(outDir, cfg, rr); err != nil {
		return rr, fmt.Errorf("write results: %w", err)
	}
	fmt.Printf("results written to %s\n", outDir)
	return rr, nil
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
// all of them. The first non-nil error is returned.
func runGenerators(ctx context.Context, gens []*loadgen.Generator, profiles []loadgen.LoadProfile) error {
	if len(gens) == 1 {
		return gens[0].Run(ctx, profiles[0])
	}
	errCh := make(chan error, len(gens))
	var wg sync.WaitGroup
	for i, g := range gens {
		wg.Add(1)
		go func(g *loadgen.Generator, p loadgen.LoadProfile) {
			defer wg.Done()
			errCh <- g.Run(ctx, p)
		}(g, profiles[i])
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
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
		v, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64)
		return v
	}
	n, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("BENCH_RESOURCE_CONTAINERS")))
	if n <= 0 {
		man.Caveats = append(man.Caveats,
			"resource budget not reported by the deploy script: container limits are unverified, so this run's hardware share is unknown")
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

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
	"github.com/juicedcore/bench/pkg/metrics"
	"github.com/juicedcore/bench/pkg/workloads"
)

// PhaseResult is the aggregate for one load phase.
type PhaseResult struct {
	Name       string          `json:"name"`
	OfferedTPS int             `json:"offered_tps"` // nominal target for the phase
	Window     WindowInfo      `json:"window"`
	Result     metrics.Result  `json:"result"`
}

// WindowInfo records the measurement window bounds relative to phase start.
type WindowInfo struct {
	PhaseStart time.Time `json:"phase_start"`
	WinStart   time.Time `json:"win_start"`
	WinEnd     time.Time `json:"win_end"`
}

// RunResult is the full output of one benchmark run.
type RunResult struct {
	Manifest     Manifest        `json:"manifest"`
	Phases       []PhaseResult   `json:"phases"`
	Headline     *metrics.Result `json:"headline"` // hold phase for sweeps, the single phase otherwise
	SaturationTPS int            `json:"saturation_tps,omitempty"`
	NativeScrapes []metrics.NativeScrape `json:"native_scrapes,omitempty"`
	SystemSamples []metrics.SystemSample `json:"system_samples,omitempty"`
}

// Options tunes engine behaviour not expressed in the run config.
type Options struct {
	// ProfileDir overrides deploy/profiles.
	ProfileDir string
	// DryRun builds everything and prints the plan without submitting load.
	DryRun bool
	// Caveats are appended to the manifest (e.g. resource-starvation notes).
	Caveats []string
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
		RunName:         cfg.Name,
		Platform:        cfg.Platform,
		Workload:        cfg.Workload,
		Profile:         cfg.Profile,
		Normalized:      cfg.Normalized,
		StartedAt:       started,
		HarnessGitSHA:   harnessGitSHA(),
		PlatformVersion: "unknown",
		StateDB:         topo.EffectiveStateDB(cfg.Normalized),
		OrdererBatch:    topo.OrdererBatch,
		ResourceLimit:   topo.PerContainer,
		Nodes:           topo.Nodes,
		Seed:            cfg.Load.Seed,
		KeySpace:        cfg.Load.KeySpace,
		KeyDistribution: cfg.Load.KeyDistribution,
		ZipfianConstant: cfg.Load.ZipfianConstant,
		ReadWriteRatio:  cfg.Load.ReadWriteRatio,
		ValueSizeBytes:  cfg.Load.ValueSizeBytes,
		WarmupSec:       cfg.Metrics.Warmup.D().Seconds(),
		CooldownSec:     cfg.Metrics.Cooldown.D().Seconds(),
		Caveats:         opt.Caveats,
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
		return &RunResult{Manifest: man}, nil
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
	gen := &loadgen.Generator{Adapter: ad, Source: wl, Collector: collector}

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
	rr := &RunResult{Manifest: man}
	warm := cfg.Metrics.Warmup.D()
	cool := cfg.Metrics.Cooldown.D()

	for _, ph := range phases {
		phaseStart := time.Now()
		if err := gen.Run(ctx, ph.profile); err != nil && ctx.Err() != nil {
			break
		}
		phaseEnd := time.Now()

		w := metrics.Window{Start: phaseStart.Add(warm), End: phaseEnd.Add(-cool)}
		if ph.noWindowTrim {
			w = metrics.Window{Start: phaseStart, End: phaseEnd}
		}
		if !w.End.After(w.Start) {
			// phase shorter than warmup+cooldown: measure the whole thing
			w = metrics.Window{Start: phaseStart, End: phaseEnd}
		}
		res := collector.Aggregate(w)
		pr := PhaseResult{
			Name:       ph.name,
			OfferedTPS: ph.profile.TargetTPS,
			Window:     WindowInfo{PhaseStart: phaseStart, WinStart: w.Start, WinEnd: w.End},
			Result:     res,
		}
		rr.Phases = append(rr.Phases, pr)

		if ph.name == "hold" || len(phases) == 1 {
			cp := res
			rr.Headline = &cp
		}
	}

	// Determine saturation: highest sweep step whose failure rate stayed under threshold.
	if cfg.Load.Sweep.Enabled {
		rr.SaturationTPS = detectSaturation(rr.Phases, cfg.Load.Sweep.MaxFailRate)
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

	man.EndedAt = time.Now()
	rr.Manifest = man

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
		// hold phase target is filled in after saturation detection at runtime;
		// here we approximate with the top step * HoldFrac so a plan is printable.
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

func detectSaturation(phases []PhaseResult, maxFail float64) int {
	best := 0
	for _, ph := range phases {
		if len(ph.Name) < 6 || ph.Name[:6] != "sweep-" {
			continue
		}
		if ph.Result.FailureRate <= maxFail && ph.OfferedTPS > best {
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

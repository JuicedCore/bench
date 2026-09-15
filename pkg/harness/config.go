// Package harness orchestrates a benchmark run: it loads a run config, resolves
// the resource profile, builds the workload and load phases, drives the adapter
// through the load generator, aggregates the measurement window, and writes the
// results + manifest. See docs/architecture/overview.md.
package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a YAML-friendly time.Duration ("30s", "5m", "1h30m").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// RunConfig is one benchmark run, parsed from configs/*.yaml.
type RunConfig struct {
	Name     string `yaml:"name"`
	Platform string `yaml:"platform"` // fabric-cft | fabric-bft | drunix | fabricx | neuchain
	Workload string `yaml:"workload"`
	Profile  string `yaml:"profile"` // local | gcp-small | gcp-full

	// Normalized selects the cross-platform comparison config set (identical
	// orderer batch params, LevelDB, same crypto). false => platform-native run.
	Normalized bool `yaml:"normalized"`

	Load    LoadConfig    `yaml:"load"`
	Metrics MetricsConfig `yaml:"metrics"`
	System  SystemConfig  `yaml:"system_metrics"`

	// Adapter carries platform-specific keys verbatim.
	Adapter map[string]any `yaml:"adapter"`

	// explicit* record that a zero-valued sweep threshold was written in the
	// file, so applyDefaults honours "max_fail_rate: 0" instead of replacing it.
	explicitMaxFailRate bool
	explicitGoodput     bool
}

// LoadConfig is the load section. It supports either a single phase (open/closed
// loop) or the probe-and-sweep methodology when Sweep is set.
type LoadConfig struct {
	Mode string `yaml:"mode"` // open-loop | closed-loop

	// Single-phase open-loop:
	StartTPS  int      `yaml:"start_tps"`
	TargetTPS int      `yaml:"target_tps"`
	RampFrom  int      `yaml:"ramp_from"`
	RampTo    int      `yaml:"ramp_to"`
	RampDur   Duration `yaml:"ramp_duration"`
	HoldDur   Duration `yaml:"hold_duration"`

	// Single-phase closed-loop:
	Workers int `yaml:"workers"`

	// Shared:
	KeyDistribution string   `yaml:"key_distribution"` // uniform | zipfian | fixed
	KeySpace        int      `yaml:"key_space"`
	ZipfianConstant float64  `yaml:"zipfian_constant"`
	ReadWriteRatio  float64  `yaml:"read_write_ratio"`
	ValueSizeBytes  int      `yaml:"value_size_bytes"`
	FinalityWait    Duration `yaml:"finality_wait"`
	Seed            int64    `yaml:"seed"`

	// Probe-and-sweep. When Sweep.Enabled the run does: probe -> stepped sweep ->
	// hold at hold_fraction of the highest step that held (see stepVerdict).
	Sweep SweepConfig `yaml:"sweep"`
}

// SweepConfig configures the probe-and-sweep methodology
// (docs/architecture/metrics-methodology.md).
type SweepConfig struct {
	Enabled     bool     `yaml:"enabled"`
	ProbeTPS    int      `yaml:"probe_tps"`
	ProbeDur    Duration `yaml:"probe_duration"`
	Steps       []int    `yaml:"steps"`
	StepDur     Duration `yaml:"step_duration"`
	HoldFrac    float64  `yaml:"hold_fraction"` // e.g. 0.9
	HoldDur     Duration `yaml:"hold_duration"`
	MaxFailRate float64  `yaml:"max_fail_rate"` // step is "sustained" if fail rate <= this

	// GoodputRatio is the fraction of a step's nominal offered rate that must
	// actually be confirmed for the step to count as held. Failure rate alone is
	// not enough: a platform that falls behind usually does not *fail*
	// transactions, it just commits fewer of them, and the generator ends up
	// submitting fewer too - so fail_rate stays at 0.0000 even when confirmed
	// throughput has collapsed. Default 0.95.
	GoodputRatio float64 `yaml:"goodput_ratio"`
	// MaxSendGapMs rejects a step whose send-gap p99 exceeds it: the generator fell
	// behind its own schedule, so the step measured the generator, not the
	// platform. Default 50, the reject threshold in fairness-guarantees.md.
	MaxSendGapMs float64 `yaml:"max_send_gap_ms"`

	// AbortAfterFailedSteps stops offering further steps once this many in a row
	// have failed to hold (any rule in stepVerdict), then goes straight to hold. This is what lets
	// every platform share one ladder: a platform that saturates at step 2 stops
	// there instead of spending the rest of the run failing steps 3..N. Skipped
	// steps are recorded in the manifest. Explicit 0 disables (run the whole
	// ladder); a pointer so that an explicit 0 is distinguishable from unset.
	AbortAfterFailedSteps *int `yaml:"abort_after_failed_steps"`
}

// MetricsConfig controls windowing and output.
type MetricsConfig struct {
	// Fixed absolute durations, NOT percentages, so every run discards the same
	// slice (docs/decisions/adr — metrics-methodology).
	Warmup       Duration `yaml:"warmup"`
	Cooldown     Duration `yaml:"cooldown"`
	OutputDir    string   `yaml:"output_dir"`
	OutputFormat string   `yaml:"output_format"` // json | csv
}

// SystemConfig controls resource sampling.
type SystemConfig struct {
	Enabled        bool     `yaml:"enabled"`
	SampleInterval Duration `yaml:"sample_interval"`
	PrometheusURL  string   `yaml:"prometheus_url"`
	CadvisorURL    string   `yaml:"cadvisor_url"`
	ContainerNames []string `yaml:"container_names"` // docker stats name filter
}

// LoadRunConfig reads and validates a run config file. `${VAR}` and `$VAR`
// references in the file are expanded from the environment first, so deploy
// scripts can emit a connection.env that `scripts/run-all.sh` sources before a
// run. An undefined variable expands to the empty string: the normalized configs
// carry every adapter's keys and rely on that to leave the other platforms' keys
// inert. The unset names are logged at debug level; a key the adapter actually
// needs is caught by that adapter's own validation.
//
// Decoding is strict: a key that is not part of the schema (a typo such as
// `warmpu:` or `target_tsp:`) is an error rather than a silently ignored line
// that leaves a default in place. Every error names the file.
func LoadRunConfig(path string) (*RunConfig, error) {
	c, err := loadRunConfig(path)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

func loadRunConfig(path string) (*RunConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var unset []string
	b = []byte(os.Expand(string(b), func(k string) string {
		v, ok := os.LookupEnv(k)
		if !ok {
			unset = append(unset, k)
		}
		return v
	}))
	if len(unset) > 0 {
		sort.Strings(unset)
		slog.Debug("config references unset environment variables (expanded to empty)",
			"config", path, "vars", strings.Join(dedupe(unset), ","))
	}
	var c RunConfig
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse: %w (keys must match the run-config schema; see CONFIGS.md)", err)
	}
	// Explicit zeros that are meaningful must survive applyDefaults.
	var present struct {
		Load struct {
			Sweep struct {
				MaxFailRate  *float64 `yaml:"max_fail_rate"`
				GoodputRatio *float64 `yaml:"goodput_ratio"`
			} `yaml:"sweep"`
		} `yaml:"load"`
	}
	_ = yaml.Unmarshal(b, &present) // already parsed strictly above; cannot fail differently
	c.explicitMaxFailRate = present.Load.Sweep.MaxFailRate != nil
	c.explicitGoodput = present.Load.Sweep.GoodputRatio != nil

	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func (c *RunConfig) applyDefaults() {
	if c.Load.KeySpace == 0 {
		c.Load.KeySpace = 100000
	}
	if c.Load.KeyDistribution == "" {
		c.Load.KeyDistribution = "uniform"
	}
	if c.Load.ValueSizeBytes == 0 {
		c.Load.ValueSizeBytes = 64
	}
	if c.Load.Seed == 0 {
		c.Load.Seed = 1
	}
	if c.Load.FinalityWait == 0 {
		c.Load.FinalityWait = Duration(30 * time.Second)
	}
	if c.Metrics.Warmup == 0 {
		c.Metrics.Warmup = Duration(30 * time.Second)
	}
	if c.Metrics.Cooldown == 0 {
		c.Metrics.Cooldown = Duration(15 * time.Second)
	}
	if c.Metrics.OutputFormat == "" {
		c.Metrics.OutputFormat = "json"
	}
	if c.Metrics.OutputDir == "" {
		c.Metrics.OutputDir = "./results"
	}
	if c.System.SampleInterval == 0 {
		c.System.SampleInterval = Duration(time.Second)
	}
	// When the load generator is not the platform host (scripts/gcp-run.sh runs
	// benchrunner on a separate VM), Prometheus lives on the platform host. The
	// environment names it there without editing the shared normalized configs.
	if v := strings.TrimSpace(os.Getenv("BENCH_PROMETHEUS_URL")); v != "" {
		c.System.PrometheusURL = v
	}
	if c.System.Enabled && c.System.PrometheusURL == "" {
		c.System.PrometheusURL = "http://localhost:9090"
	}
	if c.Load.Sweep.Enabled {
		if c.Load.Sweep.ProbeTPS == 0 {
			c.Load.Sweep.ProbeTPS = 10
		}
		if c.Load.Sweep.ProbeDur == 0 {
			c.Load.Sweep.ProbeDur = Duration(30 * time.Second)
		}
		if c.Load.Sweep.StepDur == 0 {
			c.Load.Sweep.StepDur = Duration(60 * time.Second)
		}
		if c.Load.Sweep.HoldFrac == 0 {
			c.Load.Sweep.HoldFrac = 0.9
		}
		if c.Load.Sweep.HoldDur == 0 {
			c.Load.Sweep.HoldDur = Duration(5 * time.Minute)
		}
		if c.Load.Sweep.MaxFailRate == 0 && !c.explicitMaxFailRate {
			c.Load.Sweep.MaxFailRate = 0.02
		}
		if c.Load.Sweep.GoodputRatio == 0 && !c.explicitGoodput {
			c.Load.Sweep.GoodputRatio = 0.95
		}
		if c.Load.Sweep.MaxSendGapMs == 0 {
			c.Load.Sweep.MaxSendGapMs = 50
		}
		if len(c.Load.Sweep.Steps) == 0 {
			c.Load.Sweep.Steps = []int{100, 500, 1000, 2000, 5000, 10000, 20000}
		}
		if c.Load.Sweep.AbortAfterFailedSteps == nil {
			n := 2
			c.Load.Sweep.AbortAfterFailedSteps = &n
		}
	}
}

// KnownKeyDistributions lists the accepted load.key_distribution values.
var KnownKeyDistributions = []string{"uniform", "zipfian", "fixed"}

// validate rejects configs that would otherwise run with a silently corrected or
// meaningless value. Each message names the offending key as it appears in YAML.
func (c *RunConfig) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	l := c.Load

	if c.Platform == "" {
		bad("platform is required")
	}
	if c.Workload == "" {
		bad("workload is required")
	}
	switch l.Mode {
	case "", "open-loop", "closed-loop":
	default:
		bad("load.mode must be open-loop or closed-loop, got %q", l.Mode)
	}
	if !l.Sweep.Enabled {
		if l.Mode == "closed-loop" && l.Workers <= 0 {
			bad("closed-loop requires load.workers > 0")
		}
		if l.Mode != "closed-loop" && l.TargetTPS <= 0 && l.RampTo <= 0 {
			bad("open-loop requires load.target_tps or load.ramp_to")
		}
	}
	if l.TargetTPS > 0 && l.RampTo > 0 {
		bad("load.target_tps (%d) and load.ramp_to (%d) are both set; ramp_to would silently win - set only one", l.TargetTPS, l.RampTo)
	}
	if l.RampTo <= 0 && (l.RampFrom > 0 || l.RampDur > 0) {
		bad("load.ramp_from / load.ramp_duration have no effect without load.ramp_to")
	}
	if l.Mode == "closed-loop" && (l.TargetTPS > 0 || l.RampTo > 0) {
		bad("load.target_tps / load.ramp_to are open-loop settings; closed-loop is driven by load.workers")
	}
	for name, v := range map[string]int{
		"load.start_tps": l.StartTPS, "load.target_tps": l.TargetTPS, "load.ramp_from": l.RampFrom,
		"load.ramp_to": l.RampTo, "load.workers": l.Workers, "load.key_space": l.KeySpace,
		"load.value_size_bytes": l.ValueSizeBytes,
	} {
		if v < 0 {
			bad("%s must not be negative, got %d", name, v)
		}
	}
	for name, d := range map[string]Duration{
		"load.ramp_duration": l.RampDur, "load.hold_duration": l.HoldDur, "load.finality_wait": l.FinalityWait,
		"metrics.warmup": c.Metrics.Warmup, "metrics.cooldown": c.Metrics.Cooldown,
		"system_metrics.sample_interval": c.System.SampleInterval,
		"load.sweep.probe_duration":      l.Sweep.ProbeDur, "load.sweep.step_duration": l.Sweep.StepDur,
		"load.sweep.hold_duration": l.Sweep.HoldDur,
	} {
		if d < 0 {
			bad("%s must not be negative, got %s", name, d.D())
		}
	}
	if !contains(KnownKeyDistributions, l.KeyDistribution) {
		bad("load.key_distribution %q is not one of %s", l.KeyDistribution, strings.Join(KnownKeyDistributions, ", "))
	}
	if l.ZipfianConstant != 0 && l.ZipfianConstant <= 1 {
		bad("load.zipfian_constant must be > 1 (math/rand.Zipf requirement), got %g", l.ZipfianConstant)
	}
	if l.ReadWriteRatio < 0 || l.ReadWriteRatio > 1 {
		bad("load.read_write_ratio must be in [0,1] (1 = all reads), got %g", l.ReadWriteRatio)
	}
	switch c.Metrics.OutputFormat {
	case "json", "csv":
	default:
		bad("metrics.output_format must be json or csv, got %q", c.Metrics.OutputFormat)
	}

	if s := l.Sweep; s.Enabled {
		if l.Mode == "closed-loop" {
			bad("load.sweep is an open-loop methodology (it offers TPS steps); remove load.sweep or use mode: open-loop")
		}
		if s.ProbeTPS <= 0 {
			bad("load.sweep.probe_tps must be > 0, got %d", s.ProbeTPS)
		}
		for i, st := range s.Steps {
			if st <= 0 {
				bad("load.sweep.steps[%d] must be > 0, got %d", i, st)
			}
			if i > 0 && st <= s.Steps[i-1] {
				bad("load.sweep.steps must be strictly ascending: steps[%d]=%d follows %d", i, st, s.Steps[i-1])
			}
		}
		if s.HoldFrac <= 0 || s.HoldFrac > 1 {
			bad("load.sweep.hold_fraction must be in (0,1], got %g", s.HoldFrac)
		}
		if s.MaxFailRate < 0 || s.MaxFailRate > 1 {
			bad("load.sweep.max_fail_rate must be in [0,1] (a fraction, not a percent), got %g", s.MaxFailRate)
		}
		if s.GoodputRatio < 0 || s.GoodputRatio > 1 {
			bad("load.sweep.goodput_ratio must be in [0,1], got %g", s.GoodputRatio)
		}
		if s.MaxSendGapMs <= 0 {
			bad("load.sweep.max_send_gap_ms must be > 0, got %g", s.MaxSendGapMs)
		}
		if s.AbortAfterFailedSteps != nil && *s.AbortAfterFailedSteps < 0 {
			bad("load.sweep.abort_after_failed_steps must be >= 0 (0 disables), got %d", *s.AbortAfterFailedSteps)
		}
	}
	return errors.Join(errs...)
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Package harness orchestrates a benchmark run: it loads a run config, resolves
// the resource profile, builds the workload and load phases, drives the adapter
// through the load generator, aggregates the measurement window, and writes the
// results + manifest. See docs/architecture/overview.md.
package harness

import (
	"fmt"
	"os"
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
}

// LoadConfig is the load section. It supports either a single phase (open/closed
// loop) or the probe-and-sweep methodology when Sweep is set.
type LoadConfig struct {
	Mode string `yaml:"mode"` // open-loop | closed-loop

	// Single-phase open-loop:
	StartTPS int      `yaml:"start_tps"`
	TargetTPS int     `yaml:"target_tps"`
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
	// hold at 90% of the highest step whose failure rate stayed under Sweep.MaxFailRate.
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
// run. An undefined variable expands to the empty string.
func LoadRunConfig(path string) (*RunConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(os.Expand(string(b), func(k string) string { return os.Getenv(k) }))
	var c RunConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
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
		if c.Load.Sweep.MaxFailRate == 0 {
			c.Load.Sweep.MaxFailRate = 0.02
		}
		if len(c.Load.Sweep.Steps) == 0 {
			c.Load.Sweep.Steps = []int{100, 500, 1000, 2000, 5000, 10000, 20000}
		}
	}
}

func (c *RunConfig) validate() error {
	if c.Platform == "" {
		return fmt.Errorf("platform is required")
	}
	if c.Workload == "" {
		return fmt.Errorf("workload is required")
	}
	switch c.Load.Mode {
	case "", "open-loop", "closed-loop":
	default:
		return fmt.Errorf("load.mode must be open-loop or closed-loop, got %q", c.Load.Mode)
	}
	if !c.Load.Sweep.Enabled {
		if c.Load.Mode == "closed-loop" && c.Load.Workers <= 0 {
			return fmt.Errorf("closed-loop requires load.workers > 0")
		}
		if c.Load.Mode != "closed-loop" && c.Load.TargetTPS <= 0 && c.Load.RampTo <= 0 {
			return fmt.Errorf("open-loop requires load.target_tps or load.ramp_to")
		}
	}
	return nil
}

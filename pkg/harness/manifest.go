package harness

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/metrics"
)

// CryptoInfo is re-exported from the adapters package so callers of harness do
// not need to import adapters just to read a manifest.
type CryptoInfo = adapters.CryptoInfo

// Manifest is the reproducibility record written next to every run's results.
// If two runs disagree, the manifests explain why. Everything here is a fairness
// lever identified during review (see docs/architecture/fairness-guarantees.md).
type Manifest struct {
	RunName    string    `json:"run_name"`
	Platform   string    `json:"platform"`
	Workload   string    `json:"workload"`
	Profile    string    `json:"profile"`
	Normalized bool      `json:"normalized"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`

	HarnessGitSHA string `json:"harness_git_sha"`

	// Platform identity.
	PlatformVersion string `json:"platform_version"` // git tag / image tag as reported by the adapter

	// Fairness levers.
	//
	// StateDB is the world-state backend the platform ACTUALLY ran on, taken
	// from BENCH_ACTUAL_STATE_DB when the deploy script reports it. StateDBRequested
	// is what the run config asked for (normalized runs always request leveldb).
	// They differ where a platform cannot honour the request - Drunix's up.sh
	// always deploys YugabyteDB - and recording only the request would make the
	// manifest claim a parity it does not have.
	StateDB          string       `json:"state_db"`
	StateDBRequested string       `json:"state_db_requested"`
	OrdererBatch     OrdererBatch `json:"orderer_batch"`
	Crypto           CryptoInfo   `json:"crypto"`
	// Resources as actually applied by the deploy script (lib.sh apply_budget):
	// the profile's total budget split across the platform's real containers -
	// CPU evenly, memory by role weight. Zero values mean the deploy did not
	// report limits, which the engine caveats - see applyResourceEnv.
	// ResourceLimit.Memory is set only by deploys that split memory evenly.
	ResourceLimit Limits `json:"resource_limit_per_container"`
	// ResourceMemory is the memory limit applied to each container, and
	// ResourceMemoryWeights the role weights that produced it.
	ResourceMemory        map[string]string `json:"resource_memory_by_container,omitempty"`
	ResourceMemoryWeights string            `json:"resource_memory_weights,omitempty"`
	ResourceContainers    int               `json:"resource_containers"`
	ResourceCPUsTotal     float64           `json:"resource_cpus_total"`
	ResourceMemTotalGB    float64           `json:"resource_memory_total_gb"`
	Nodes                 map[string]int    `json:"nodes"`

	// Load determinism. Generators matters here too: generator i uses seed+i.
	Generators      int     `json:"generators"`
	Seed            int64   `json:"seed"`
	KeySpace        int     `json:"key_space"`
	KeyDistribution string  `json:"key_distribution"`
	ZipfianConstant float64 `json:"zipfian_constant"`
	ReadWriteRatio  float64 `json:"read_write_ratio"`
	ValueSizeBytes  int     `json:"value_size_bytes"`

	// Windowing.
	WarmupSec   float64 `json:"warmup_sec"`
	CooldownSec float64 `json:"cooldown_sec"`

	// Sweep outcome. SkippedSteps lists the sweep steps the early-abort rule
	// never offered, so a truncated ladder is self-describing rather than
	// looking like a shorter config.
	SkippedSteps []int `json:"skipped_steps,omitempty"`

	// ContainerFailures lists platform containers that exited or were OOM-killed
	// during the run. Any entry means the run ended early on a platform failure
	// and has no headline.
	ContainerFailures []metrics.ContainerFailure `json:"container_failures,omitempty"`

	// Free-form caveats surfaced in the report (e.g. "fabricx local: Arma
	// starved, not comparable to published ceiling").
	Caveats []string `json:"caveats,omitempty"`
}

// Write persists the manifest as pretty JSON.
func (m *Manifest) Write(path string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// harnessGitSHA returns the current commit of the harness repo, or "unknown".
func harnessGitSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

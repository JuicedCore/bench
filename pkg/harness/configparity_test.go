package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/harness"

	_ "github.com/juicedcore/bench/pkg/adapters/drunix"
	_ "github.com/juicedcore/bench/pkg/adapters/fabric"
	_ "github.com/juicedcore/bench/pkg/adapters/fabricx"
	_ "github.com/juicedcore/bench/pkg/adapters/neuchain"
)

// comparedPlatforms are the platforms a normalized run is meant to be comparable
// across (docs/architecture/fairness-guarantees.md). mock is excluded: it is a
// self-test simulator, not a platform under comparison.
var comparedPlatforms = []string{"fabric-cft", "fabric-bft", "drunix", "fabricx", "neuchain"}

func normalizedConfigDir(t *testing.T) string {
	t.Helper()
	// Tests run from the package dir; the configs live at the repo root.
	dir, err := filepath.Abs(filepath.Join("..", "..", "configs", "normalized"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("configs/normalized not found: %v", err)
	}
	return dir
}

func normalizedConfigs(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(normalizedConfigDir(t), "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no configs in configs/normalized")
	}
	return paths
}

// Every file in configs/normalized must actually be a normalized run. A config
// that quietly sets normalized:false would be excluded from the comparison
// bucket by the reporter while still sitting in the directory that claims
// otherwise - which is how Fabric-X ended up outside the comparison.
func TestNormalizedConfigsAreNormalized(t *testing.T) {
	for _, p := range normalizedConfigs(t) {
		cfg, err := harness.LoadRunConfig(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if !cfg.Normalized {
			t.Errorf("%s: normalized=false in configs/normalized", filepath.Base(p))
		}
		if cfg.Load.Seed != 1 {
			t.Errorf("%s: seed=%d, want 1 (one seed drives every generator across platforms)", filepath.Base(p), cfg.Load.Seed)
		}
		if cfg.Load.ValueSizeBytes != 64 {
			t.Errorf("%s: value_size_bytes=%d, want an explicit 64", filepath.Base(p), cfg.Load.ValueSizeBytes)
		}
	}
}

// The point of the shared-file layout: for a given mode, every fairness lever is
// identical no matter which platform runs it. This is near-tautological while one
// file serves all platforms, and that is deliberate - it fails loudly the moment
// anyone reintroduces per-platform copies of a normalized config.
func TestNormalizedLeversIdenticalAcrossPlatforms(t *testing.T) {
	type levers struct {
		workload, keyDist            string
		keySpace, valueSize          int
		zipfian, readWrite, maxFail  float64
		seed                         int64
		warmupSec, cooldownSec       float64
		probeTPS, stepCount, holdInt int
		holdFrac                     float64
	}

	for _, p := range normalizedConfigs(t) {
		name := filepath.Base(p)
		var first *levers
		for _, plat := range comparedPlatforms {
			cfg, err := harness.LoadRunConfig(p)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			cfg.Platform = plat // what --platform does
			got := levers{
				workload:    cfg.Workload,
				keyDist:     cfg.Load.KeyDistribution,
				keySpace:    cfg.Load.KeySpace,
				valueSize:   cfg.Load.ValueSizeBytes,
				zipfian:     cfg.Load.ZipfianConstant,
				readWrite:   cfg.Load.ReadWriteRatio,
				maxFail:     cfg.Load.Sweep.MaxFailRate,
				seed:        cfg.Load.Seed,
				warmupSec:   cfg.Metrics.Warmup.D().Seconds(),
				cooldownSec: cfg.Metrics.Cooldown.D().Seconds(),
				probeTPS:    cfg.Load.Sweep.ProbeTPS,
				stepCount:   len(cfg.Load.Sweep.Steps),
				holdInt:     cfg.Load.TargetTPS,
				holdFrac:    cfg.Load.Sweep.HoldFrac,
			}
			if first == nil {
				cp := got
				first = &cp
				continue
			}
			if got != *first {
				t.Errorf("%s: fairness levers differ for platform %s:\n got %+v\nwant %+v", name, plat, got, *first)
			}
		}
	}
}

// Non-smoke normalized configs must use the 30s/15s window the fairness contract
// specifies (fairness-guarantees.md). quick-smoke is the documented exception: a
// 30s phase cannot carry a 30s warmup.
func TestNormalizedWindowsMatchFairnessContract(t *testing.T) {
	for _, p := range normalizedConfigs(t) {
		name := filepath.Base(p)
		if name == "quick-smoke.yaml" {
			continue
		}
		cfg, err := harness.LoadRunConfig(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w := cfg.Metrics.Warmup.D().Seconds(); w != 30 {
			t.Errorf("%s: warmup=%.0fs, want 30s", name, w)
		}
		if c := cfg.Metrics.Cooldown.D().Seconds(); c != 15 {
			t.Errorf("%s: cooldown=%.0fs, want 15s", name, c)
		}
	}
}

// The union adapter block is what lets one file serve every platform. Each
// adapter must accept it: unknown keys ignored, and keys whose ${VAR} was unset
// (so they arrive as empty strings) must not be mistaken for real values.
func TestUnionAdapterBlockLoadsOnEveryPlatform(t *testing.T) {
	for _, p := range normalizedConfigs(t) {
		name := filepath.Base(p)
		cfg, err := harness.LoadRunConfig(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, plat := range comparedPlatforms {
			ad, err := adapters.New(plat)
			if err != nil {
				t.Errorf("%s/%s: %v", name, plat, err)
				continue
			}
			if ad.Name() == "" {
				t.Errorf("%s/%s: adapter reports no name", name, plat)
			}
		}
		if len(cfg.Adapter) == 0 {
			t.Errorf("%s: empty adapter block", name)
		}
		// Every platform's keys must be present, or that platform silently falls
		// back to adapter defaults instead of reading its connection.env.
		for _, key := range []string{
			"peer_endpoint", "channel", "chaincode", // fabric family
			"broadcast_endpoint", "deliver_endpoint", "signing_key_path", // fabricx
			"block_servers", "query_endpoint", "table_name", // neuchain
		} {
			if _, ok := cfg.Adapter[key]; !ok {
				t.Errorf("%s: union adapter block is missing %q", name, key)
			}
		}
	}
}

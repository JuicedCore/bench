package harness_test

import (
	"os"
	"path/filepath"
	"strings"
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
			"broadcast_endpoint", "deliver_endpoint", "query_endpoint", "signing_key_path", // fabricx (query_endpoint shared with neuchain)
			"block_servers", "table_name", // neuchain
		} {
			if _, ok := cfg.Adapter[key]; !ok {
				t.Errorf("%s: union adapter block is missing %q", name, key)
			}
		}
	}
}

func nativeConfigDir(t *testing.T) string {
	return configDir(t, "native")
}

func configDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "configs", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("configs/%s not found: %v", name, err)
	}
	return dir
}

// Native configs are per-platform, normalized:false, and named after the
// platform they target so run-all.sh can skip cross-product runs.
func TestNativeConfigsAreNative(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(nativeConfigDir(t), "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no configs in configs/native")
	}
	want := map[string]bool{"fabric-cft": false, "fabric-bft": false, "drunix": false, "fabricx": false, "neuchain": false}
	for _, p := range paths {
		cfg, err := harness.LoadRunConfig(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if cfg.Normalized {
			t.Errorf("%s: normalized=true in configs/native", filepath.Base(p))
		}
		stem := strings.TrimSuffix(filepath.Base(p), ".yaml")
		if cfg.Platform != stem {
			t.Errorf("%s: platform=%s, want filename stem %s (run-all.sh matches on this)", filepath.Base(p), cfg.Platform, stem)
		}
		if _, ok := want[cfg.Platform]; ok {
			want[cfg.Platform] = true
		}
	}
	for plat, seen := range want {
		if !seen {
			t.Errorf("configs/native is missing %s.yaml", plat)
		}
	}
}

// Native kv-mixed is a second per-platform native set: same levers as
// configs/native, kv-mixed at 50% reads, never mixed into the kv-write ceiling.
func TestNativeKVMixedConfigs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(configDir(t, "native-kv-mixed"), "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no configs in configs/native-kv-mixed")
	}
	want := map[string]bool{"fabric-cft": false, "fabric-bft": false, "drunix": false, "fabricx": false, "neuchain": false}
	for _, p := range paths {
		cfg, err := harness.LoadRunConfig(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if cfg.Normalized {
			t.Errorf("%s: normalized=true in configs/native-kv-mixed", filepath.Base(p))
		}
		if cfg.Workload != "kv-mixed" {
			t.Errorf("%s: workload=%s, want kv-mixed", filepath.Base(p), cfg.Workload)
		}
		if cfg.Name != "native-kv-mixed" {
			t.Errorf("%s: name=%s, want native-kv-mixed", filepath.Base(p), cfg.Name)
		}
		if cfg.Load.ReadWriteRatio != 0.5 {
			t.Errorf("%s: read_write_ratio=%g, want 0.5", filepath.Base(p), cfg.Load.ReadWriteRatio)
		}
		stem := strings.TrimSuffix(filepath.Base(p), ".yaml")
		if cfg.Platform != stem {
			t.Errorf("%s: platform=%s, want filename stem %s", filepath.Base(p), cfg.Platform, stem)
		}
		if _, ok := want[cfg.Platform]; ok {
			want[cfg.Platform] = true
		}
	}
	for plat, seen := range want {
		if !seen {
			t.Errorf("configs/native-kv-mixed is missing %s.yaml", plat)
		}
	}
}

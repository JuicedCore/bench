package harness

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is a resource budget + per-platform topology, parsed from
// deploy/profiles/<name>.yaml. The benchrunner "setup" command turns a platform
// entry into Docker Compose --cpus/--memory overrides.
type Profile struct {
	Name   string `yaml:"name"`
	Budget Budget `yaml:"budget"`
	// Platforms maps platform name -> topology.
	Platforms map[string]PlatformTopo `yaml:"platforms"`
	// GCP holds machine types for scripts/gcp-run.sh; the harness ignores it.
	GCP map[string]any `yaml:"gcp"`
	// Anchors collects top-level keys starting with "_" - YAML anchor holders
	// such as _orderer_batch_normalized. Any other unknown key is an error.
	Anchors map[string]any `yaml:",inline"`
}

// Budget is the host resource envelope reserved for the platform under test,
// after host OS, load generator and monitoring are subtracted.
type Budget struct {
	TotalCPUs     float64 `yaml:"total_cpus"`
	TotalMemoryGB float64 `yaml:"total_memory_gb"`
	// LoadGenCPUs is what the load generator process gets; high-ceiling
	// platforms may override this in their entry.
	LoadGenCPUs float64 `yaml:"load_gen_cpus"`
}

// PlatformTopo is one platform's deployed topology and fairness parameters.
type PlatformTopo struct {
	// Nodes is the container topology the deploy script really starts, role ->
	// count. Informational (recorded in the manifest); the enforced resource lever
	// is Budget, split evenly across those containers by lib.sh apply_budget.
	Nodes map[string]int `yaml:"nodes"`
	// LoadGenCPUs overrides Budget.LoadGenCPUs for this platform.
	LoadGenCPUs float64 `yaml:"load_gen_cpus"`
	// StateDB is the world-state backend for native runs ("leveldb","couchdb","yugabyte").
	// Normalized runs always force leveldb regardless of this value.
	StateDB string `yaml:"state_db"`
	// OrdererBatch pins the block-cutting parameters
	// (docs/decisions/adr-011-orderer-batch-params.md).
	OrdererBatch OrdererBatch `yaml:"orderer_batch"`
}

// Limits is a CPU/memory ceiling for one container.
type Limits struct {
	CPUs   float64 `yaml:"cpus"`
	Memory string  `yaml:"memory"` // docker form, e.g. "2g"
}

// OrdererBatch is the Fabric-family block-cutting configuration. Ignored for
// NeuChain. For normalized runs these MUST be identical across fabric-cft,
// fabric-bft, drunix and fabricx.
type OrdererBatch struct {
	MaxMessageCount   int    `yaml:"max_message_count"`
	AbsoluteMaxBytes  string `yaml:"absolute_max_bytes"`
	PreferredMaxBytes string `yaml:"preferred_max_bytes"`
	BatchTimeout      string `yaml:"batch_timeout"`
}

// LoadProfile reads deploy/profiles/<name>.yaml. dir defaults to "deploy/profiles".
// Decoding is strict and the profile is validated, so a missing budget or batch
// parameter fails here instead of turning into an unconstrained or unpinned run.
func LoadProfile(name, dir string) (*Profile, error) {
	if dir == "" {
		dir = filepath.Join("deploy", "profiles")
	}
	if name == "" {
		return nil, fmt.Errorf("no profile selected: set profile: in the run config or pass --profile (available in %s: %s)", dir, availableProfiles(dir))
	}
	path := filepath.Join(dir, name+".yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("profile %q not found at %s (available: %s; run from the repo root or pass --profile-dir)", name, path, availableProfiles(dir))
		}
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile %s: parse: %w", path, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	return &p, nil
}

func availableProfiles(dir string) string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(m) == 0 {
		return "none - directory missing or empty"
	}
	names := make([]string, len(m))
	for i, f := range m {
		names[i] = strings.TrimSuffix(filepath.Base(f), ".yaml")
	}
	return strings.Join(names, ", ")
}

// fabricFamily are the platforms whose deploy scripts write orderer_batch into
// the channel config; for them the batch parameters are a fairness lever and
// must be present.
var fabricFamily = map[string]bool{"fabric-cft": true, "fabric-bft": true, "drunix": true, "fabricx": true}

func (p *Profile) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	for k := range p.Anchors {
		if !strings.HasPrefix(k, "_") {
			bad("unknown top-level key %q (only name, budget, gcp, platforms, and _-prefixed anchor holders are allowed)", k)
		}
	}
	if p.Budget.TotalCPUs <= 0 {
		bad("budget.total_cpus must be > 0, got %g", p.Budget.TotalCPUs)
	}
	if p.Budget.TotalMemoryGB <= 0 {
		bad("budget.total_memory_gb must be > 0, got %g", p.Budget.TotalMemoryGB)
	}
	if p.Budget.LoadGenCPUs < 0 {
		bad("budget.load_gen_cpus must not be negative, got %g", p.Budget.LoadGenCPUs)
	}
	if len(p.Platforms) == 0 {
		bad("platforms: no platform entries")
	}
	names := make([]string, 0, len(p.Platforms))
	for n := range p.Platforms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := p.Platforms[n]
		if len(t.Nodes) == 0 {
			bad("platforms.%s.nodes is empty", n)
		}
		if t.LoadGenCPUs < 0 {
			bad("platforms.%s.load_gen_cpus must not be negative", n)
		}
		switch t.StateDB {
		case "", "leveldb", "couchdb", "yugabyte":
		default:
			bad("platforms.%s.state_db %q is not one of leveldb, couchdb, yugabyte", n, t.StateDB)
		}
		if fabricFamily[n] {
			ob := t.OrdererBatch
			if ob.MaxMessageCount <= 0 {
				bad("platforms.%s.orderer_batch.max_message_count must be > 0 (block cutting is a fairness lever, adr-011)", n)
			}
			if ob.BatchTimeout == "" || ob.AbsoluteMaxBytes == "" || ob.PreferredMaxBytes == "" {
				bad("platforms.%s.orderer_batch needs batch_timeout, absolute_max_bytes and preferred_max_bytes (adr-011)", n)
			}
		}
	}
	return errors.Join(errs...)
}

// Topo returns the topology for a platform, or an error if the profile has none.
func (p *Profile) Topo(platform string) (PlatformTopo, error) {
	t, ok := p.Platforms[platform]
	if !ok {
		have := make([]string, 0, len(p.Platforms))
		for n := range p.Platforms {
			have = append(have, n)
		}
		sort.Strings(have)
		return PlatformTopo{}, fmt.Errorf("profile %q has no entry for platform %q (it defines: %s)", p.Name, platform, strings.Join(have, ", "))
	}
	return t, nil
}

// EffectiveStateDB applies the normalized-run override: normalized => leveldb.
func (t PlatformTopo) EffectiveStateDB(normalized bool) string {
	if normalized {
		return "leveldb"
	}
	if t.StateDB == "" {
		return "leveldb"
	}
	return t.StateDB
}

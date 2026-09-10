package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile is a resource budget + per-platform topology, parsed from
// deploy/profiles/<name>.yaml. The benchrunner "setup" command turns a platform
// entry into Docker Compose --cpus/--memory overrides.
type Profile struct {
	Name   string        `yaml:"name"`
	Budget Budget        `yaml:"budget"`
	// Platforms maps platform name -> topology.
	Platforms map[string]PlatformTopo `yaml:"platforms"`
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

// PlatformTopo is node counts + per-container limits for one platform.
type PlatformTopo struct {
	Nodes        map[string]int `yaml:"nodes"` // role -> count, e.g. {"peer":1,"orderer":1}
	PerContainer Limits         `yaml:"per_container"`
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
func LoadProfile(name, dir string) (*Profile, error) {
	if dir == "" {
		dir = filepath.Join("deploy", "profiles")
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		p.Name = name
	}
	return &p, nil
}

// Topo returns the topology for a platform, or an error if the profile has none.
func (p *Profile) Topo(platform string) (PlatformTopo, error) {
	t, ok := p.Platforms[platform]
	if !ok {
		return PlatformTopo{}, fmt.Errorf("profile %q has no entry for platform %q", p.Name, platform)
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

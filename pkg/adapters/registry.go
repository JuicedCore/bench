package adapters

import (
	"fmt"
	"sort"
	"sync"
)

// Factory builds a fresh adapter instance. Registered by each adapter package's
// init() so the harness never imports platform SDKs directly.
type Factory func() PlatformAdapter

var (
	regMu sync.RWMutex
	reg   = map[string]Factory{}
)

// Register makes an adapter available by name. Panics on duplicate name.
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[name]; dup {
		panic("adapters: duplicate registration for " + name)
	}
	reg[name] = f
}

// New returns a fresh adapter for the given platform name.
func New(name string) (PlatformAdapter, error) {
	regMu.RLock()
	f, ok := reg[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no adapter registered for %q (have: %v)", name, Registered())
	}
	return f(), nil
}

// Registered lists the names of all registered adapters, sorted.
func Registered() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for n := range reg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Package loadgen turns a LoadProfile into a stream of transactions submitted to
// a PlatformAdapter, in either open-loop (fixed offered rate) or closed-loop
// (fixed concurrency) mode. Key selection is deterministic given the seed so
// every platform sees the identical access pattern and contention profile
// (see docs/architecture/fairness-guarantees.md).
package loadgen

import (
	"math"
	"math/rand"
	"sync"
)

// KeyGen returns a key index in [0, keySpace) on each call. Implementations must
// be safe for concurrent use.
type KeyGen interface {
	Next() int
}

// NewKeyGen builds a KeyGen from a distribution name. seed makes it reproducible.
// Supported: "uniform", "zipfian", "fixed" (always key 0).
func NewKeyGen(dist string, keySpace int, zipfConst float64, seed int64) KeyGen {
	if keySpace < 1 {
		keySpace = 1
	}
	switch dist {
	case "fixed":
		return &fixedGen{}
	case "zipfian":
		if zipfConst <= 1.0 {
			zipfConst = 1.01 // math/rand.Zipf requires s > 1
		}
		return &zipfGen{
			z:   rand.NewZipf(rand.New(rand.NewSource(seed)), zipfConst, 1, uint64(keySpace-1)),
			max: keySpace,
		}
	default: // "uniform"
		return &uniformGen{r: rand.New(rand.NewSource(seed)), n: keySpace}
	}
}

type fixedGen struct{}

func (f *fixedGen) Next() int { return 0 }

type uniformGen struct {
	mu sync.Mutex
	r  *rand.Rand
	n  int
}

func (u *uniformGen) Next() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.r.Intn(u.n)
}

type zipfGen struct {
	mu  sync.Mutex
	z   *rand.Zipf
	max int
}

func (z *zipfGen) Next() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	v := int(z.z.Uint64())
	if v >= z.max {
		v = z.max - 1
	}
	return v
}

// ReadWriteGen decides per transaction whether it is a read, given a ratio in
// [0,1] where 1.0 means all reads.
type ReadWriteGen struct {
	mu    sync.Mutex
	r     *rand.Rand
	ratio float64
}

// NewReadWriteGen returns a deterministic read/write chooser.
func NewReadWriteGen(readRatio float64, seed int64) *ReadWriteGen {
	return &ReadWriteGen{r: rand.New(rand.NewSource(seed)), ratio: math.Max(0, math.Min(1, readRatio))}
}

// IsRead reports whether the next transaction should be a read.
func (g *ReadWriteGen) IsRead() bool {
	if g.ratio <= 0 {
		return false
	}
	if g.ratio >= 1 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.r.Float64() < g.ratio
}

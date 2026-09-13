// Package workloads defines the normalized units of work the harness generates.
// A Workload is deterministic given its seed: the same run config produces the
// same transaction stream on every platform, so contention and key-access
// patterns are identical (see docs/workloads/normalized.md).
package workloads

import (
	"fmt"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/loadgen"
)

// Workload builds one Transaction per call to Next. It is safe for concurrent use.
type Workload interface {
	// Name is the identifier used in configs and results.
	Name() string
	// Next returns the transaction for the given monotonic sequence number.
	Next(seq uint64) *adapters.Transaction
	// KeyFor returns the string key for a key index, so verification code can
	// re-derive keys without generating transactions.
	KeyFor(idx int) string
}

// Config is the subset of a run configuration a workload needs.
type Config struct {
	KeySpace        int
	KeyDistribution string
	ZipfianConstant float64
	ReadWriteRatio  float64
	ValueSizeBytes  int
	Seed            int64
}

// New constructs a workload by name. Supported normalized workloads:
//
//	"kv-write"  - all TxWrite
//	"kv-read"   - all TxRead
//	"kv-mixed"  - TxRead/TxWrite per ReadWriteRatio
//	"transfer"  - TxTransfer between two keys drawn from the key space
//
// Platform-native workloads are registered in their adapter packages.
func New(name string, cfg Config) (Workload, error) {
	kg := loadgen.NewKeyGen(cfg.KeyDistribution, cfg.KeySpace, cfg.ZipfianConstant, cfg.Seed)
	switch name {
	case "kv-write":
		return &kv{name: name, keys: kg, rw: nil, valSize: valSize(cfg), forceWrite: true}, nil
	case "kv-read":
		return &kv{name: name, keys: kg, rw: loadgen.NewReadWriteGen(1.0, cfg.Seed+1), valSize: valSize(cfg)}, nil
	case "kv-mixed":
		return &kv{name: name, keys: kg, rw: loadgen.NewReadWriteGen(cfg.ReadWriteRatio, cfg.Seed+1), valSize: valSize(cfg)}, nil
	case "transfer":
		return &transfer{
			name:  name,
			src:   kg,
			dst:   loadgen.NewKeyGen(cfg.KeyDistribution, cfg.KeySpace, cfg.ZipfianConstant, cfg.Seed+7),
			space: max(cfg.KeySpace, 1),
		}, nil
	default:
		return nil, fmt.Errorf("unknown workload %q", name)
	}
}

func valSize(cfg Config) int {
	if cfg.ValueSizeBytes <= 0 {
		return 64
	}
	return cfg.ValueSizeBytes
}

type kv struct {
	name       string
	keys       loadgen.KeyGen
	rw         *loadgen.ReadWriteGen // nil => always write
	valSize    int
	forceWrite bool
}

func (w *kv) Name() string        { return w.name }
func (w *kv) KeyFor(i int) string { return fmt.Sprintf("key-%09d", i) }

func (w *kv) Next(seq uint64) *adapters.Transaction {
	idx := w.keys.Next()
	key := w.KeyFor(idx)
	if !w.forceWrite && w.rw != nil && w.rw.IsRead() {
		return &adapters.Transaction{Kind: adapters.TxRead, Key: key, Seq: seq}
	}
	val := make([]byte, w.valSize)
	// deterministic, non-constant payload so state size grows realistically
	for i := range val {
		val[i] = byte('a' + int((seq+uint64(i))%26))
	}
	return &adapters.Transaction{Kind: adapters.TxWrite, Key: key, Value: val, Seq: seq}
}

type transfer struct {
	name  string
	src   loadgen.KeyGen
	dst   loadgen.KeyGen
	space int
}

func (w *transfer) Name() string        { return w.name }
func (w *transfer) KeyFor(i int) string { return fmt.Sprintf("acct-%09d", i) }

func (w *transfer) Next(seq uint64) *adapters.Transaction {
	s := w.src.Next()
	d := w.dst.Next()
	if d == s {
		d = (d + 1) % w.space
	}
	return &adapters.Transaction{
		Kind:    adapters.TxTransfer,
		Key:     w.KeyFor(s),
		DestKey: w.KeyFor(d),
		Amount:  1,
		Seq:     seq,
	}
}

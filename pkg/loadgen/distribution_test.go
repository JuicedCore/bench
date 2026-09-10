package loadgen

import "testing"

func TestKeyGenDeterministic(t *testing.T) {
	a := NewKeyGen("uniform", 1000, 0, 42)
	b := NewKeyGen("uniform", 1000, 0, 42)
	for i := 0; i < 1000; i++ {
		if a.Next() != b.Next() {
			t.Fatalf("uniform generator not deterministic at i=%d", i)
		}
	}
}

func TestFixedGenAlwaysZero(t *testing.T) {
	g := NewKeyGen("fixed", 1000, 0, 1)
	for i := 0; i < 100; i++ {
		if got := g.Next(); got != 0 {
			t.Fatalf("fixed gen returned %d, want 0", got)
		}
	}
}

func TestZipfianSkew(t *testing.T) {
	g := NewKeyGen("zipfian", 1000, 1.3, 7)
	counts := map[int]int{}
	const n = 50000
	for i := 0; i < n; i++ {
		counts[g.Next()]++
	}
	// Key 0 is the hottest; it should take a large share under s=1.3.
	if float64(counts[0])/float64(n) < 0.05 {
		t.Fatalf("zipfian not skewed enough: key 0 share = %.3f", float64(counts[0])/float64(n))
	}
	for k := range counts {
		if k < 0 || k >= 1000 {
			t.Fatalf("zipfian produced out-of-range key %d", k)
		}
	}
}

func TestReadWriteRatio(t *testing.T) {
	g := NewReadWriteGen(0.3, 99)
	reads := 0
	const n = 20000
	for i := 0; i < n; i++ {
		if g.IsRead() {
			reads++
		}
	}
	frac := float64(reads) / float64(n)
	if frac < 0.27 || frac > 0.33 {
		t.Fatalf("read fraction %.3f not near 0.30", frac)
	}
}

package harness

import "testing"

func TestLoadRealLocalProfile(t *testing.T) {
	p, err := LoadProfile("local", "../../deploy/profiles")
	if err != nil {
		t.Fatalf("load local profile: %v", err)
	}
	for _, plat := range []string{"fabric-cft", "fabric-bft", "drunix", "fabricx", "neuchain", "mock"} {
		if _, ok := p.Platforms[plat]; !ok {
			t.Errorf("local profile missing platform %q", plat)
		}
	}
	// Fabric-family normalized orderer batch must be identical (shared anchor).
	cft, _ := p.Topo("fabric-cft")
	bft, _ := p.Topo("fabric-bft")
	dru, _ := p.Topo("drunix")
	fx, _ := p.Topo("fabricx")
	if cft.OrdererBatch != bft.OrdererBatch || cft.OrdererBatch != dru.OrdererBatch || cft.OrdererBatch != fx.OrdererBatch {
		t.Errorf("Fabric-family orderer_batch drifted:\n cft=%+v\n bft=%+v\n drunix=%+v\n fabricx=%+v",
			cft.OrdererBatch, bft.OrdererBatch, dru.OrdererBatch, fx.OrdererBatch)
	}
	if cft.OrdererBatch.MaxMessageCount == 0 {
		t.Error("orderer_batch not populated from anchor")
	}
}

func TestEffectiveStateDBNormalizedOverride(t *testing.T) {
	topo := PlatformTopo{StateDB: "yugabyte"}
	if got := topo.EffectiveStateDB(true); got != "leveldb" {
		t.Errorf("normalized run must force leveldb, got %q", got)
	}
	if got := topo.EffectiveStateDB(false); got != "yugabyte" {
		t.Errorf("native run must honour configured db, got %q", got)
	}
	empty := PlatformTopo{}
	if got := empty.EffectiveStateDB(false); got != "leveldb" {
		t.Errorf("empty state_db should default leveldb, got %q", got)
	}
}

func TestTopoMissingPlatform(t *testing.T) {
	p := &Profile{Name: "t", Platforms: map[string]PlatformTopo{}}
	if _, err := p.Topo("nope"); err == nil {
		t.Error("expected error for unknown platform")
	}
}

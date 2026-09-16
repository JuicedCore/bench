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
	if dru.StateDB != "yugabyte" {
		t.Errorf("drunix native state_db=%q, want yugabyte", dru.StateDB)
	}
	if fx.StateDB != "postgres" {
		t.Errorf("fabricx native state_db=%q, want postgres", fx.StateDB)
	}
	if cft.OrdererBatchNative.MaxMessageCount == 0 || cft.OrdererBatchNative.MaxMessageCount == cft.OrdererBatch.MaxMessageCount {
		t.Errorf("fabric-cft native batch should be tuned separately, got %+v vs %+v", cft.OrdererBatchNative, cft.OrdererBatch)
	}
	if bft.OrdererBatchNative.MaxMessageCount == cft.OrdererBatchNative.MaxMessageCount {
		t.Errorf("BFT native batch should differ from CFT, both max_message_count=%d", bft.OrdererBatchNative.MaxMessageCount)
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

func TestEffectiveOrdererBatchNative(t *testing.T) {
	topo := PlatformTopo{
		OrdererBatch:       OrdererBatch{MaxMessageCount: 100, BatchTimeout: "1s"},
		OrdererBatchNative: OrdererBatch{MaxMessageCount: 500, BatchTimeout: "500ms"},
	}
	if got := topo.EffectiveOrdererBatch(true); got.MaxMessageCount != 100 {
		t.Errorf("normalized must use shared batch, got %+v", got)
	}
	if got := topo.EffectiveOrdererBatch(false); got.MaxMessageCount != 500 {
		t.Errorf("native must use native batch, got %+v", got)
	}
	plain := PlatformTopo{OrdererBatch: OrdererBatch{MaxMessageCount: 100}}
	if got := plain.EffectiveOrdererBatch(false); got.MaxMessageCount != 100 {
		t.Errorf("native without a native block must fall back, got %+v", got)
	}
}

func TestTopoMissingPlatform(t *testing.T) {
	p := &Profile{Name: "t", Platforms: map[string]PlatformTopo{}}
	if _, err := p.Topo("nope"); err == nil {
		t.Error("expected error for unknown platform")
	}
}

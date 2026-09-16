package monitoring

import (
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

func TestMergeCadvisorIntoSamples(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	samples := []metrics.SystemSample{
		{T: t0, CPUPercent: 150, MemBytes: 300, Containers: 2},
		{T: t0.Add(5 * time.Second), CPUPercent: 180, MemBytes: 320, Containers: 2},
	}
	cpu := []Series{{
		Legend: "peer0.org1",
		Points: []Point{{T: t0, V: 1.0}, {T: t0.Add(5 * time.Second), V: 1.2}},
	}}
	mem := []Series{{
		Legend: "peer0.org1",
		Points: []Point{{T: t0, V: 200 << 20}, {T: t0.Add(5 * time.Second), V: 210 << 20}},
	}}
	mergeCadvisorIntoSamples(samples, cpu, mem)
	if len(samples[0].ByContainer) != 1 || samples[0].ByContainer[0].Name != "peer0.org1" {
		t.Fatalf("sample0: %+v", samples[0].ByContainer)
	}
	if samples[0].ByContainer[0].CPUPercent != 100 {
		t.Fatalf("cores 1.0 should store as 100%%, got %v", samples[0].ByContainer[0].CPUPercent)
	}
	if samples[1].ByContainer[0].CPUPercent != 120 {
		t.Fatalf("cores 1.2 should store as 120%%, got %v", samples[1].ByContainer[0].CPUPercent)
	}

	already := []metrics.SystemSample{{
		T: t0, ByContainer: []metrics.ContainerUsage{{Name: "keep-me", CPUPercent: 9}},
	}}
	mergeCadvisorIntoSamples(already, cpu, mem)
	if already[0].ByContainer[0].Name != "keep-me" {
		t.Fatal("must not overwrite samples that already have a breakdown")
	}
}

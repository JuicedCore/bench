package monitoring

import (
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

func TestContainerRegexPromQLSafe(t *testing.T) {
	if got := containerRegex(nil); got != "^$" {
		t.Fatalf("empty names: %q", got)
	}
	got := containerRegex([]string{"lp1.", "cp.org", "peer"})
	want := `.*lp1\\..*|.*cp\\.org.*|.*peer.*`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSystemSampleChartSpecsPerContainer(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	specs := systemSampleChartSpecs([]metrics.SystemSample{{
		T: t0, CPUPercent: 150, MemBytes: 300, Containers: 2,
		ByContainer: []metrics.ContainerUsage{
			{Name: "peer0.org1.example.com", CPUPercent: 100, MemBytes: 200},
			{Name: "orderer.example.com", CPUPercent: 50, MemBytes: 100},
		},
	}})
	if len(specs) != 2 {
		t.Fatalf("charts = %d", len(specs))
	}
	if len(specs[0].Series) != 2 {
		t.Fatalf("cpu series = %d", len(specs[0].Series))
	}
	if specs[0].Series[0].Legend != "orderer" || specs[0].Series[1].Legend != "peer0.org1" {
		t.Fatalf("cpu legends = %q, %q", specs[0].Series[0].Legend, specs[0].Series[1].Legend)
	}

	old := systemSampleChartSpecs([]metrics.SystemSample{{T: t0, CPUPercent: 80, MemBytes: 50}})
	if len(old[0].Series) != 1 || old[0].Series[0].Legend != "all containers" {
		t.Fatalf("legacy fallback: %+v", old[0].Series)
	}
}

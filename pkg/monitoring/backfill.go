package monitoring

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

const cadvisorBackfillSlack = 20 * time.Second

// BackfillByContainer writes per-container CPU/memory onto samples that only
// have the summed docker-stats fields, using cAdvisor series from Prometheus
// over the same window. CPU is stored in docker-stats units (100 = one core).
// No-op when ByContainer is already present or Prometheus returns nothing.
func BackfillByContainer(ctx context.Context, client *Client, samples []metrics.SystemSample, start, end time.Time, containerNames []string) error {
	if client == nil || len(samples) == 0 || len(metrics.SampledContainerNames(samples)) > 0 {
		return nil
	}
	if end.Before(start) || end.Equal(start) {
		return nil
	}
	step := clampStep(end.Sub(start))
	re := containerRegex(containerNames)
	cpuExpr := fmt.Sprintf(`sum by (name) (rate(container_cpu_usage_seconds_total{name=~"%s"}[15s]))`, re)
	memExpr := fmt.Sprintf(`sum by (name) (container_memory_rss{name=~"%s"})`, re)

	cpu, err := client.QueryRange(ctx, cpuExpr, "{{name}}", start, end, step)
	if err != nil {
		return fmt.Errorf("cAdvisor CPU: %w", err)
	}
	mem, err := client.QueryRange(ctx, memExpr, "{{name}}", start, end, step)
	if err != nil {
		return fmt.Errorf("cAdvisor memory: %w", err)
	}
	mergeCadvisorIntoSamples(samples, cpu, mem)
	return nil
}

func mergeCadvisorIntoSamples(samples []metrics.SystemSample, cpu, mem []Series) {
	if len(metrics.SampledContainerNames(samples)) > 0 {
		return
	}
	cpu = filterBackfillSeries(cpu)
	mem = filterBackfillSeries(mem)
	names := unionSeriesNames(cpu, mem)
	if len(names) == 0 {
		return
	}
	cpuAt := indexSeries(cpu)
	memAt := indexSeries(mem)
	for i := range samples {
		t := samples[i].T
		var usage []metrics.ContainerUsage
		for _, name := range names {
			u := metrics.ContainerUsage{Name: name}
			have := false
			if v, ok := valueAt(cpuAt[name], t, cadvisorBackfillSlack); ok {
				u.CPUPercent = v * 100 // cores -> docker-stats percent
				have = true
			}
			if v, ok := valueAt(memAt[name], t, cadvisorBackfillSlack); ok && v >= 0 && !math.IsNaN(v) {
				u.MemBytes = uint64(v)
				have = true
			}
			if have {
				usage = append(usage, u)
			}
		}
		if len(usage) == 0 {
			continue
		}
		sort.Slice(usage, func(a, b int) bool { return usage[a].Name < usage[b].Name })
		samples[i].ByContainer = usage
		if samples[i].Containers == 0 {
			samples[i].Containers = len(usage)
		}
	}
}

func filterBackfillSeries(in []Series) []Series {
	var out []Series
	for _, s := range in {
		if s.Legend == "" || strings.Contains(s.Legend, "{{") || skipBackfillName(s.Legend) {
			continue
		}
		if len(s.Points) == 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

func skipBackfillName(name string) bool {
	n := strings.ToLower(name)
	for _, junk := range []string{
		"cadvisor", "prometheus", "grafana", "node_exporter", "node-exporter",
		"monitoring", "feast", "headlamp", "cassandra",
	} {
		if strings.Contains(n, junk) {
			return true
		}
	}
	return false
}

func unionSeriesNames(groups ...[]Series) []string {
	seen := map[string]struct{}{}
	for _, g := range groups {
		for _, s := range g {
			if s.Legend != "" {
				seen[s.Legend] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func indexSeries(in []Series) map[string][]Point {
	out := make(map[string][]Point, len(in))
	for _, s := range in {
		out[s.Legend] = s.Points
	}
	return out
}

func valueAt(points []Point, t time.Time, slack time.Duration) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	best := points[0]
	bestD := absDuration(t.Sub(best.T))
	for _, p := range points[1:] {
		d := absDuration(t.Sub(p.T))
		if d < bestD {
			best, bestD = p, d
		}
	}
	if bestD > slack {
		return 0, false
	}
	return best.V, true
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

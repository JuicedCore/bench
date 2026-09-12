// Package monitoring builds a standalone, self-contained HTML report for one
// benchmark run: experiment settings + harness result metrics (from the
// RunResult already in memory) plus Prometheus/Grafana metrics for the run's
// exact time window, rendered as embedded PNG charts via scripts/render_run_report.py.
//
// GenerateReport never fails a run: any Prometheus/query/render problem is
// folded into a banner and placeholders inside the HTML itself, not returned
// as an error. Only "couldn't write the file at all" is a real error.
package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/juicedcore/bench/pkg/harness"
	"github.com/juicedcore/bench/pkg/metrics"
)

const (
	overviewDashboard    = "dashboards/grafana/overview.json"
	perPlatformDashboard = "dashboards/grafana/per-platform.json"
	defaultPrometheusURL = "http://localhost:9090"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// GenerateReport writes monitoring-report.html into rr.OutDir.
func GenerateReport(ctx context.Context, cfg *harness.RunConfig, rr *harness.RunResult) error {
	data := buildReportData(ctx, cfg, rr)
	return writeReportHTML(filepath.Join(rr.OutDir, "monitoring-report.html"), data)
}

func buildReportData(ctx context.Context, cfg *harness.RunConfig, rr *harness.RunResult) *reportData {
	data := &reportData{
		RunName:         cfg.Name,
		Platform:        cfg.Platform,
		PlatformVersion: rr.Manifest.PlatformVersion,
		Workload:        cfg.Workload,
		Profile:         cfg.Profile,
		Normalized:      cfg.Normalized,
		Manifest:        rr.Manifest,
		Headline:        rr.Headline,
		SaturationTPS:   rr.SaturationTPS,
		NativeScrapes:   rr.NativeScrapes,
		GeneratedAt:     time.Now(),
	}
	for _, ph := range rr.Phases {
		data.Phases = append(data.Phases, buildPhaseView(ph))
	}

	if rr.Headline != nil {
		data.Banner = append(data.Banner, headlineWarnings(rr.Headline)...)
	}
	for _, c := range rr.Manifest.Caveats {
		data.Banner = append(data.Banner, "caveat: "+c)
	}

	if !cfg.System.Enabled {
		data.MonitoringSkipped = "system_metrics.enabled is false in this run's config"
		return data
	}

	// System-samples charts (docker-stats fallback) don't depend on Prometheus
	// at all, so they're built unconditionally and rendered in the same batch
	// as whatever Prometheus charts succeed - this is the one chart that still
	// shows up even when the monitoring stack is down.
	systemCharts := systemSampleChartSpecs(rr.SystemSamples)

	var overviewCharts, perPlatformCharts []chartSpec
	var chartErrs []string

	promURL := cfg.System.PrometheusURL
	if promURL == "" {
		promURL = defaultPrometheusURL
	}
	client := &Client{BaseURL: promURL, HTTP: &http.Client{Timeout: 10 * time.Second}}

	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	pingErr := client.Ping(pctx)
	cancel()
	if pingErr != nil {
		data.MonitoringUnavailable = fmt.Sprintf("Prometheus unreachable at %s: %v", promURL, pingErr)
		data.Banner = append(data.Banner, data.MonitoringUnavailable)
	} else if overviewPanels, perPlatformPanels, overviewNotes, perPlatformNotes, err := LoadPanels(overviewDashboard, perPlatformDashboard); err != nil {
		msg := fmt.Sprintf("could not load dashboard panel definitions: %v", err)
		data.MonitoringUnavailable = msg
		data.Banner = append(data.Banner, msg)
	} else {
		data.OverviewNote = strings.Join(overviewNotes, " ")
		data.PerPlatformNote = strings.Join(perPlatformNotes, " ")

		start, end := rr.Manifest.StartedAt, rr.Manifest.EndedAt
		step := clampStep(end.Sub(start))
		containerRe := containerRegex(cfg.System.ContainerNames)
		platformRe := jobLabelFor(cfg.Platform)

		var errs []string
		overviewCharts, errs = queryPanels(ctx, client, overviewPanels, start, end, step, containerRe, "")
		chartErrs = append(chartErrs, errs...)
		perPlatformCharts, errs = queryPanels(ctx, client, perPlatformPanels, start, end, step, "", platformRe)
		chartErrs = append(chartErrs, errs...)
	}

	all := append(append(append([]chartSpec{}, overviewCharts...), perPlatformCharts...), systemCharts...)
	rendered, err := renderCharts(ctx, chartRequest{Charts: all})
	if err != nil {
		msg := fmt.Sprintf("chart rendering failed: %v", err)
		data.Banner = append(data.Banner, msg)
	}

	data.OverviewCharts = toChartViews(overviewCharts, rendered)
	data.PerPlatformCharts = toChartViews(perPlatformCharts, rendered)
	data.SystemSampleCharts = toChartViews(systemCharts, rendered)
	data.ChartErrors = chartErrs
	data.Banner = append(data.Banner, chartErrs...)

	return data
}

// systemSampleChartSpecs builds CPU% and memory charts straight from the
// harness's own docker-stats sampler (metrics.SystemSample), independent of
// Prometheus. Empty input still yields specs with zero series, which render
// as the normal "no data" placeholder rather than an error.
func systemSampleChartSpecs(samples []metrics.SystemSample) []chartSpec {
	cpu := chartSpec{ID: "harness-cpu-sampling", Title: systemCPUChartTitle, Unit: "percent"}
	mem := chartSpec{ID: "harness-mem-sampling", Title: systemMemChartTitle, Unit: "bytes"}
	if len(samples) == 0 {
		return []chartSpec{cpu, mem}
	}
	cpuSeries := seriesSpec{Legend: "CPU %"}
	memSeries := seriesSpec{Legend: "RSS"}
	for _, s := range samples {
		t := float64(s.T.Unix())
		cpuSeries.Times = append(cpuSeries.Times, t)
		cpuSeries.Values = append(cpuSeries.Values, s.CPUPercent)
		memSeries.Times = append(memSeries.Times, t)
		memSeries.Values = append(memSeries.Values, float64(s.MemBytes))
	}
	cpu.Series = []seriesSpec{cpuSeries}
	mem.Series = []seriesSpec{memSeries}
	return []chartSpec{cpu, mem}
}

func clampStep(window time.Duration) time.Duration {
	step := window / 300
	if step < 5*time.Second {
		step = 5 * time.Second
	}
	if step > 15*time.Second {
		step = 15 * time.Second
	}
	return step
}

// containerRegex builds the $container substitution. PromQL's =~ is a fully
// anchored match, not substring, but cAdvisor's `name` label is the real
// docker container name (e.g. "peer0.org1.example.com"), never exactly equal
// to a short config name like "peer" - so each name is wrapped in ".*" to get
// the substring-match behaviour cfg.System.ContainerNames is documented as
// ("docker stats name filter", config.go). An empty list means the panel
// should match nothing rather than everything - "^$" never matches a real
// (non-empty) container name.
func containerRegex(names []string) string {
	if len(names) == 0 {
		return "^$"
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = ".*" + regexp.QuoteMeta(n) + ".*"
	}
	return strings.Join(parts, "|")
}

// queryPanels runs every target of every panel through Prometheus and returns
// one chartSpec per panel (possibly with zero series, meaning "no data") plus
// any query errors encountered (panel title + short error), which do not stop
// other panels/targets from being queried.
func queryPanels(ctx context.Context, client *Client, panels []Panel, start, end time.Time, step time.Duration, containerRe, platformRe string) ([]chartSpec, []string) {
	var specs []chartSpec
	var errs []string
	for _, p := range panels {
		spec := chartSpec{ID: slug(p.Title), Title: p.Title, Unit: p.Unit}
		for _, t := range p.Targets {
			expr := strings.ReplaceAll(t.Expr, "$container", containerRe)
			expr = strings.ReplaceAll(expr, "$platform", platformRe)

			qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			series, err := client.QueryRange(qctx, expr, t.LegendFormat, start, end, step)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", p.Title, err))
				continue
			}
			spec.Series = append(spec.Series, seriesToSpec(series)...)
		}
		specs = append(specs, spec)
	}
	return specs, errs
}

func toChartViews(specs []chartSpec, rendered map[string]string) []chartView {
	views := make([]chartView, 0, len(specs))
	for _, s := range specs {
		views = append(views, chartView{
			Title:       s.Title,
			ImgDataURI:  rendered[s.ID],
			Explanation: panelExplanations[s.Title],
			HasData:     len(s.Series) > 0,
		})
	}
	return views
}

// headlineWarnings surfaces run-level problems regardless of monitoring status.
func headlineWarnings(h *metrics.Result) []string {
	var out []string
	if h.FailureRate > 0 {
		out = append(out, fmt.Sprintf("failure rate %.2f%% (%d invalid, %d errored, %d timed out of %d submitted)",
			h.FailureRate*100, h.Invalid, h.Errored, h.TimedOut, h.Submitted))
	}
	if !h.InvariantOK {
		out = append(out, "submitted != committed+invalid+errored+timedout — result is suspect")
	}
	if p99 := h.SendGap.Percentiles["p99"]; p99 > 50 {
		out = append(out, fmt.Sprintf("load-generator send-gap p99 = %.1f ms: generator may be the bottleneck; reject this run", p99))
	}
	return out
}

func writeReportHTML(path string, data *reportData) error {
	b, err := renderTemplate(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

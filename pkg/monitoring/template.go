package monitoring

import (
	"bytes"
	_ "embed"
	"html/template"
	"time"

	"github.com/juicedcore/bench/pkg/harness"
	"github.com/juicedcore/bench/pkg/metrics"
)

//go:embed report.html.tmpl
var reportTmplSrc string

type reportData struct {
	RunName         string
	Platform        string
	PlatformVersion string
	Workload        string
	Profile         string
	Normalized      bool

	Manifest harness.Manifest
	Phases   []phaseView

	Headline      *metrics.Result
	SaturationTPS int
	NativeScrapes []metrics.NativeScrape

	Banner []string

	MonitoringSkipped     string
	MonitoringUnavailable string
	OverviewNote          string
	PerPlatformNote       string
	OverviewCharts        []chartView
	PerPlatformCharts     []chartView
	SystemSampleCharts    []chartView
	ChartErrors           []string

	GeneratedAt time.Time
}

// percentileOrder is the display order for the latency percentile table -
// must match pkg/metrics/histogram.go's `reported` spectrum. Map iteration in
// html/template sorts keys alphabetically ("p1,p10,p25,p5,..."), which is
// wrong for percentiles, so the order is fixed here instead.
var percentileOrder = []string{"p1", "p5", "p10", "p25", "p50", "p75", "p90", "p95", "p99", "p99.9", "p99.99"}

// latencyRow is one percentile's worth of the four latency metrics, in ms.
type latencyRow struct {
	Pct     string
	E2E     float64
	Submit  float64
	Commit  float64
	SendGap float64
}

// phaseView wraps one harness.PhaseResult with the full percentile table
// precomputed, so the template never has to iterate a map.
type phaseView struct {
	Name        string
	OfferedTPS  int
	Result      metrics.Result
	Window      harness.WindowInfo
	LatencyRows []latencyRow
}

func buildPhaseView(ph harness.PhaseResult) phaseView {
	pv := phaseView{Name: ph.Name, OfferedTPS: ph.OfferedTPS, Result: ph.Result, Window: ph.Window}
	for _, p := range percentileOrder {
		pv.LatencyRows = append(pv.LatencyRows, latencyRow{
			Pct:     p,
			E2E:     ph.Result.E2E.Percentiles[p],
			Submit:  ph.Result.Submit.Percentiles[p],
			Commit:  ph.Result.Commit.Percentiles[p],
			SendGap: ph.Result.SendGap.Percentiles[p],
		})
	}
	return pv
}

type chartView struct {
	Title       string
	ImgDataURI  string
	Explanation string
	HasData     bool
}

// Rendered reports whether there's a PNG to show - false either because the
// query returned no data or because the render subprocess failed for this
// chart specifically, both of which the template treats as a placeholder.
func (c chartView) Rendered() bool { return c.HasData && c.ImgDataURI != "" }

var tmplFuncs = template.FuncMap{
	"pctl": func(s metrics.Snapshot, key string) float64 {
		if s.Percentiles == nil {
			return 0
		}
		return s.Percentiles[key]
	},
	"explain": func(name string) string {
		return harnessMetricExplanations[name]
	},
	"mulf":                  func(a, b float64) float64 { return a * b },
	"nativeScrapeCaveat":    func() string { return nativeScrapeCaveat },
	"headlineExplanation":   func() string { return headlineExplanation },
	"saturationExplanation": func() string { return saturationExplanation },
	"percentileTableNote":   func() string { return percentileTableNote },
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02 15:04:05 MST")
	},
	"duration": func(a, b time.Time) string {
		return b.Sub(a).Round(time.Second).String()
	},
	// safeURL marks a data: URI as trusted so html/template's URL sanitizer
	// (which otherwise rejects unrecognized data: schemes as #ZgotmplZ) embeds
	// it verbatim. Safe here: the bytes are PNGs we rendered ourselves via
	// scripts/render_run_report.py, not attacker-controlled HTML/JS.
	"safeURL": func(s string) template.URL { return template.URL(s) },
}

var reportTmpl = template.Must(template.New("report").Funcs(tmplFuncs).Parse(reportTmplSrc))

func renderTemplate(data *reportData) ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

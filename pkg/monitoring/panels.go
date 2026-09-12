package monitoring

import (
	"encoding/json"
	"os"
)

// PanelTarget is one PromQL query within a panel.
type PanelTarget struct {
	Expr         string
	LegendFormat string
}

// Panel is one chart worth of dashboard data, parsed down to the fields the
// report generator needs. Non-timeseries panels (annotations, text notes) are
// dropped by LoadPanels, except their markdown is kept separately as caveat text.
type Panel struct {
	Title   string
	Unit    string // fieldConfig.defaults.unit: "bytes" | "percentunit" | "Bps" | "s" | "bool" | "none" | ""
	Targets []PanelTarget
}

// dashboardJSON mirrors only the Grafana dashboard-JSON fields this package
// reads; gridPos, annotations, templating etc. are ignored.
type dashboardJSON struct {
	Panels []struct {
		Type    string `json:"type"`
		Title   string `json:"title"`
		Targets []struct {
			Expr         string `json:"expr"`
			LegendFormat string `json:"legendFormat"`
		} `json:"targets"`
		FieldConfig struct {
			Defaults struct {
				Unit string `json:"unit"`
			} `json:"defaults"`
		} `json:"fieldConfig"`
		Options struct {
			Content string `json:"content"`
		} `json:"options"`
	} `json:"panels"`
}

func loadDashboard(path string) (panels []Panel, notes []string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var d dashboardJSON
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, nil, err
	}
	for _, p := range d.Panels {
		if p.Type == "text" {
			if p.Options.Content != "" {
				notes = append(notes, p.Options.Content)
			}
			continue
		}
		if p.Type != "timeseries" {
			continue
		}
		panel := Panel{Title: p.Title, Unit: p.FieldConfig.Defaults.Unit}
		for _, t := range p.Targets {
			panel.Targets = append(panel.Targets, PanelTarget{Expr: t.Expr, LegendFormat: t.LegendFormat})
		}
		panels = append(panels, panel)
	}
	return panels, notes, nil
}

// LoadPanels reads the overview and per-platform dashboard JSON files and
// returns their timeseries panels plus any markdown note/caveat text found in
// each (in panel order).
func LoadPanels(overviewPath, perPlatformPath string) (overview, perPlatform []Panel, overviewNotes, perPlatformNotes []string, err error) {
	overview, overviewNotes, err = loadDashboard(overviewPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	perPlatform, perPlatformNotes, err = loadDashboard(perPlatformPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return overview, perPlatform, overviewNotes, perPlatformNotes, nil
}

// platformJobLabel maps a benchrunner platform name to the Prometheus job
// `platform` label used in deploy/docker/monitoring/prometheus.yml. fabric-cft
// and fabric-bft share one `fabric` scrape job; other platforms pass through
// unchanged. Platforms with no scrape job (e.g. mock) simply return no series.
var platformJobLabel = map[string]string{
	"fabric-cft": "fabric",
	"fabric-bft": "fabric",
}

func jobLabelFor(platform string) string {
	if v, ok := platformJobLabel[platform]; ok {
		return v
	}
	return platform
}

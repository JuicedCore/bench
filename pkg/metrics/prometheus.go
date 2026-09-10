package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/prometheus/common/expfmt"
)

// NativeScrape captures one snapshot of a platform's own Prometheus endpoint.
// These readings are INFORMATIONAL ONLY and must never enter a cross-platform
// comparison (see docs/architecture/fairness-guarantees.md). They are stored so a
// reviewer can explain *why* a platform behaved as it did.
type NativeScrape struct {
	T        time.Time          `json:"t"`
	Endpoint string             `json:"endpoint"`
	Values   map[string]float64 `json:"values"` // metric name -> summed sample value
}

// ScrapeNative fetches and parses the given /metrics endpoint. Only counters and
// gauges are kept, summed across label sets, which is enough for coarse
// "endorsement time", "blocks committed" style series.
func ScrapeNative(ctx context.Context, endpoint string) (*NativeScrape, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("empty endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("scrape %s: status %d: %s", endpoint, resp.StatusCode, body)
	}

	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, err
	}

	out := &NativeScrape{T: time.Now(), Endpoint: endpoint, Values: map[string]float64{}}
	for name, mf := range families {
		var sum float64
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				sum += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				sum += m.GetGauge().GetValue()
			case m.GetUntyped() != nil:
				sum += m.GetUntyped().GetValue()
			}
		}
		out.Values[name] = sum
	}
	return out, nil
}

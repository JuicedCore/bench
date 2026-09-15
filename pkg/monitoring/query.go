package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Point is one sample of a queried series.
type Point struct {
	T time.Time
	V float64
}

// Series is one labelled time series returned by a range query.
type Series struct {
	Legend string
	Points []Point
}

// Client talks to a Prometheus server's HTTP query API directly (not the
// platform-native /metrics text endpoints pkg/metrics.ScrapeNative reads).
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Ping checks that Prometheus is reachable and healthy.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/-/healthy", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus healthy check at %s: status %d (is the monitoring stack up? deploy/docker/monitoring/up.sh)", c.BaseURL, resp.StatusCode)
	}
	return nil
}

// QueryRange calls Prometheus's /api/v1/query_range and returns one Series per
// result label-set. legendFormat supports the simple "{{label}}" substitution
// used by this project's dashboards (no full Grafana template engine needed).
func (c *Client) QueryRange(ctx context.Context, promql, legendFormat string, start, end time.Time, step time.Duration) ([]Series, error) {
	q := url.Values{}
	q.Set("query", promql)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))

	reqURL := strings.TrimRight(c.BaseURL, "/") + "/api/v1/query_range?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("prometheus query_range at %s: status %d: %s", c.BaseURL, resp.StatusCode, msg)
	}

	var parsed struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"` // [unixSeconds(float64), value(string)]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode prometheus response from %s: %w", c.BaseURL, err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("prometheus query error: %s", parsed.Error)
	}

	var out []Series
	for _, r := range parsed.Data.Result {
		s := Series{Legend: expandLegend(legendFormat, r.Metric)}
		for _, v := range r.Values {
			ts, ok1 := v[0].(float64)
			valStr, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				continue
			}
			val, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				continue
			}
			// Prometheus emits "NaN" for a timestamp where e.g. histogram_quantile
			// had no underlying samples - not a real 0, just absent. encoding/json
			// can't marshal NaN/Inf either way, so drop these rather than send a
			// point that would blow up the render subprocess call.
			if math.IsNaN(val) || math.IsInf(val, 0) {
				continue
			}
			s.Points = append(s.Points, Point{T: time.Unix(int64(ts), 0), V: val})
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// expandLegend substitutes "{{label}}" placeholders using the series' labels.
// Falls back to the raw format string if it has no placeholders, or the metric
// name itself if the format is empty.
func expandLegend(format string, labels map[string]string) string {
	if format == "" {
		if n, ok := labels["__name__"]; ok {
			return n
		}
		return ""
	}
	out := format
	for k, v := range labels {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

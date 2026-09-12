package monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"time"
)

type chartRequest struct {
	Charts []chartSpec `json:"charts"`
}

type chartSpec struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Unit   string       `json:"unit"`
	Series []seriesSpec `json:"series"`
}

type seriesSpec struct {
	Legend string    `json:"legend"`
	Times  []float64 `json:"times"`
	Values []float64 `json:"values"`
}

// renderCharts shells out once to scripts/render_run_report.py, sending all of
// this run's charts in one batch and getting back base64 PNG data URIs keyed
// by chart id.
func renderCharts(ctx context.Context, req chartRequest) (map[string]string, error) {
	sanitizeChartRequest(&req)
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(rctx, "python3", "scripts/render_run_report.py", "--render-charts")
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("render_run_report.py: %w: %s", err, stderr.String())
	}

	var out map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("decode render_run_report.py output: %w", err)
	}
	return out, nil
}

// sanitizeChartRequest drops any NaN/Inf value pairs - encoding/json cannot
// marshal them at all, and a single bad point would otherwise fail rendering
// for every chart in the batch, not just the one with the bad value.
func sanitizeChartRequest(req *chartRequest) {
	for ci := range req.Charts {
		for si := range req.Charts[ci].Series {
			s := &req.Charts[ci].Series[si]
			times, values := s.Times[:0], s.Values[:0]
			for i, v := range s.Values {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					continue
				}
				times = append(times, s.Times[i])
				values = append(values, v)
			}
			s.Times, s.Values = times, values
		}
	}
}

func seriesToSpec(series []Series) []seriesSpec {
	out := make([]seriesSpec, 0, len(series))
	for _, s := range series {
		spec := seriesSpec{Legend: s.Legend}
		for _, p := range s.Points {
			spec.Times = append(spec.Times, float64(p.T.Unix()))
			spec.Values = append(spec.Values, p.V)
		}
		out = append(out, spec)
	}
	return out
}

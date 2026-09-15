package monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
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
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = "..." + msg[len(msg)-800:]
		}
		hint := ""
		switch {
		case errors.Is(err, exec.ErrNotFound):
			hint = " (python3 not on PATH)"
		case strings.Contains(msg, "No module named"):
			hint = " (install matplotlib: scripts/install-deps.sh, or pip install matplotlib)"
		case strings.Contains(msg, "can't open file"):
			hint = " (run benchrunner from the repository root)"
		case rctx.Err() == context.DeadlineExceeded:
			hint = " (timed out after 20s)"
		}
		return nil, fmt.Errorf("scripts/render_run_report.py: %w%s: %s", err, hint, msg)
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

package harness

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

func writeSummaryText(path string, cfg *RunConfig, rr *RunResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run:        %s\n", cfg.Name)
	fmt.Fprintf(&b, "platform:   %s  (version %s)\n", cfg.Platform, rr.Manifest.PlatformVersion)
	fmt.Fprintf(&b, "workload:   %s   normalized=%v\n", cfg.Workload, cfg.Normalized)
	fmt.Fprintf(&b, "profile:    %s   state_db=%s\n", cfg.Profile, rr.Manifest.StateDB)
	fmt.Fprintf(&b, "batch:      msgcount=%d timeout=%s preferred=%s\n",
		rr.Manifest.OrdererBatch.MaxMessageCount, rr.Manifest.OrdererBatch.BatchTimeout, rr.Manifest.OrdererBatch.PreferredMaxBytes)
	fmt.Fprintf(&b, "crypto:     sig=%s hash=%s per_tx_endorse_verify=%v\n",
		rr.Manifest.Crypto.SignatureAlg, rr.Manifest.Crypto.HashAlg, rr.Manifest.Crypto.PerTxEndorsementVerify)
	if len(rr.Manifest.Caveats) > 0 {
		fmt.Fprintf(&b, "caveats:\n")
		for _, c := range rr.Manifest.Caveats {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-12s %10s %12s %12s %10s %10s %10s %10s\n",
		"phase", "offered", "conf_tps", "fail_rate", "e2e_p50", "e2e_p99", "sub_p50", "com_p50")
	for _, ph := range rr.Phases {
		r := ph.Result
		fmt.Fprintf(&b, "%-12s %10d %12.1f %12.4f %10.2f %10.2f %10.2f %10.2f\n",
			ph.Name, ph.OfferedTPS, r.ConfirmedTPS, r.FailureRate,
			pctl(r.E2E, "p50"), pctl(r.E2E, "p99"), pctl(r.Submit, "p50"), pctl(r.Commit, "p50"))
	}
	b.WriteString("\n")
	if rr.SaturationTPS > 0 {
		fmt.Fprintf(&b, "detected saturation: ~%d offered TPS\n", rr.SaturationTPS)
	}
	if rr.Headline != nil {
		h := rr.Headline
		fmt.Fprintf(&b, "HEADLINE  confirmed_tps=%.1f  fail_rate=%.4f  e2e p50/p99=%.2f/%.2f ms  invariant_ok=%v\n",
			h.ConfirmedTPS, h.FailureRate, pctl(h.E2E, "p50"), pctl(h.E2E, "p99"), h.InvariantOK)
		if p99 := pctl(h.SendGap, "p99"); p99 > 50 {
			fmt.Fprintf(&b, "WARNING   load-generator send-gap p99 = %.1f ms: generator may be the bottleneck; reject this run\n", p99)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func pctl(s metrics.Snapshot, key string) float64 {
	if s.Percentiles == nil {
		return 0
	}
	return s.Percentiles[key]
}

func writePhaseCSV(path string, rr *RunResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"phase", "offered_tps", "confirmed_tps", "offered_tps_actual", "fail_rate",
		"committed", "submitted", "invalid", "errored", "timed_out",
		"e2e_p50_ms", "e2e_p95_ms", "e2e_p99_ms", "e2e_p99.9_ms", "submit_p50_ms", "commit_p50_ms"})
	for _, ph := range rr.Phases {
		r := ph.Result
		_ = w.Write([]string{
			ph.Name,
			strconv.Itoa(ph.OfferedTPS),
			f2(r.ConfirmedTPS), f2(r.OfferedTPS), f4(r.FailureRate),
			i64(r.Committed), i64(r.Submitted), i64(r.Invalid), i64(r.Errored), i64(r.TimedOut),
			f2(pctl(r.E2E, "p50")), f2(pctl(r.E2E, "p95")), f2(pctl(r.E2E, "p99")), f2(pctl(r.E2E, "p99.9")),
			f2(pctl(r.Submit, "p50")), f2(pctl(r.Commit, "p50")),
		})
	}
	return nil
}

func f2(f float64) string  { return strconv.FormatFloat(f, 'f', 2, 64) }
func f4(f float64) string  { return strconv.FormatFloat(f, 'f', 4, 64) }
func i64(i int64) string   { return strconv.FormatInt(i, 10) }

// ---- cross-run comparison report ----

// BuildReport scans resultsDir for result.json files and writes an HTML
// comparison table to out. Platform-native metrics are intentionally excluded
// (see docs/architecture/fairness-guarantees.md).
func BuildReport(resultsDir, out string) error {
	type row struct {
		Platform   string
		Workload   string
		Normalized bool
		When       time.Time
		ConfTPS    float64
		FailRate   float64
		E2EP50     float64
		E2EP99     float64
		Caveats    []string
	}
	var rows []row

	err := filepath.WalkDir(resultsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "result.json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var rr RunResult
		if json.Unmarshal(b, &rr) != nil || rr.Headline == nil {
			return nil
		}
		rows = append(rows, row{
			Platform:   rr.Manifest.Platform,
			Workload:   rr.Manifest.Workload,
			Normalized: rr.Manifest.Normalized,
			When:       rr.Manifest.StartedAt,
			ConfTPS:    rr.Headline.ConfirmedTPS,
			FailRate:   rr.Headline.FailureRate,
			E2EP50:     pctl(rr.Headline.E2E, "p50"),
			E2EP99:     pctl(rr.Headline.E2E, "p99"),
			Caveats:    rr.Manifest.Caveats,
		})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Workload != rows[j].Workload {
			return rows[i].Workload < rows[j].Workload
		}
		return rows[i].ConfTPS > rows[j].ConfTPS
	})

	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=utf-8><title>Benchmark comparison</title>")
	b.WriteString("<style>body{font:14px system-ui;margin:2rem}table{border-collapse:collapse}")
	b.WriteString("td,th{border:1px solid #ccc;padding:.4rem .6rem;text-align:right}th{background:#f4f4f4}")
	b.WriteString("td:first-child,td:nth-child(2){text-align:left}.caveat{color:#a60}</style>")
	b.WriteString("<h1>Cross-platform comparison (harness metrics only)</h1>")
	b.WriteString("<p>Platform-native endorsement/ordering/validation breakdowns are excluded by design.</p>")
	b.WriteString("<table><tr><th>platform<th>workload<th>norm<th>confirmed TPS<th>fail rate<th>e2e p50 ms<th>e2e p99 ms<th>when</tr>")
	for _, r := range rows {
		fmt.Fprintf(&b, "<tr><td>%s<td>%s<td>%v<td>%.1f<td>%.4f<td>%.2f<td>%.2f<td>%s</tr>",
			r.Platform, r.Workload, r.Normalized, r.ConfTPS, r.FailRate, r.E2EP50, r.E2EP99,
			r.When.Format("2006-01-02 15:04"))
		for _, c := range r.Caveats {
			fmt.Fprintf(&b, "<tr><td colspan=8 class=caveat>&#9888; %s</td></tr>", c)
		}
	}
	b.WriteString("</table>")
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

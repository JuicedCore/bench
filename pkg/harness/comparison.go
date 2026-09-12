package harness

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReportOptions scopes the cross-run comparison.
type ReportOptions struct {
	// Since drops runs that started before it. Zero keeps everything.
	Since time.Time
}

// runRecord is one result.json plus the verdict on whether it may be compared.
type runRecord struct {
	Path     string
	RR       RunResult
	Problems []string // non-empty = excluded from every aggregate
}

// platformRow aggregates one platform's valid replicates within a group.
type platformRow struct {
	Platform       string
	Valid          []runRecord
	Invalid        []runRecord
	MedConfTPS     float64
	MinConfTPS     float64
	MaxConfTPS     float64
	MedE2EP50      float64
	MedE2EP99      float64
	MedFailRate    float64
	MedSaturation  float64
	Caveats        []string
	Crypto         CryptoInfo
	PlatformVer    string
	ResourceString string
}

// comparisonGroup holds runs that are comparable with each other: same
// normalized-ness, same run config, same workload, same profile.
type comparisonGroup struct {
	Normalized bool
	RunName    string
	Workload   string
	Profile    string
	IsSweep    bool
	Rows       []*platformRow
}

// runProblems says why a run must not be compared, or nothing if it may be.
// These mirror "Rejecting a run" in docs/architecture/fairness-guarantees.md.
func runProblems(rr RunResult) []string {
	var p []string
	m, h := rr.Manifest, rr.Headline
	if m.Generators == 0 {
		// manifest.generators was introduced together with the saturation,
		// back-pressure and Fabric T3 fixes; its absence dates the run before them.
		p = append(p, "recorded before the measurement fixes (saturation rule, generator back-pressure, Fabric T3) - not comparable")
	}
	if h == nil {
		return append(p, "no headline result")
	}
	if !h.InvariantOK {
		p = append(p, "invariant broken: a submitted transaction never reached a terminal state")
	}
	if gap := pctl(h.SendGap, "p99"); gap > 50 {
		p = append(p, fmt.Sprintf("load generator fell behind (send-gap p99 %.0f ms > 50)", gap))
	}
	if m.ResourceContainers == 0 {
		p = append(p, "resource budget not reported by deploy - hardware share unverified")
	}
	if top, isSweep := topSweepStep(rr); isSweep {
		switch {
		case rr.SaturationTPS == 0:
			p = append(p, "no sweep step held - headline is a floor reading, not a saturation figure")
		case rr.SaturationTPS == top && len(m.SkippedSteps) == 0:
			p = append(p, fmt.Sprintf("never saturated within the ladder - %d TPS is a lower bound, not a knee", top))
		}
	}
	return p
}

func topSweepStep(rr RunResult) (int, bool) {
	top, found := 0, false
	for _, ph := range rr.Phases {
		if strings.HasPrefix(ph.Name, "sweep-") {
			found = true
			if ph.OfferedTPS > top {
				top = ph.OfferedTPS
			}
		}
	}
	return top, found
}

func loadRunRecords(resultsDir string, opt ReportOptions) ([]runRecord, error) {
	var recs []runRecord
	err := filepath.WalkDir(resultsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "result.json" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var rr RunResult
		if json.Unmarshal(b, &rr) != nil {
			return nil
		}
		if !opt.Since.IsZero() && rr.Manifest.StartedAt.Before(opt.Since) {
			return nil
		}
		recs = append(recs, runRecord{Path: path, RR: rr, Problems: runProblems(rr)})
		return nil
	})
	return recs, err
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func buildComparison(recs []runRecord) []*comparisonGroup {
	type gkey struct {
		norm              bool
		name, wl, profile string
	}
	groups := map[gkey]*comparisonGroup{}
	rows := map[gkey]map[string]*platformRow{}

	for _, r := range recs {
		m := r.RR.Manifest
		k := gkey{m.Normalized, m.RunName, m.Workload, m.Profile}
		g, ok := groups[k]
		if !ok {
			g = &comparisonGroup{Normalized: m.Normalized, RunName: m.RunName, Workload: m.Workload, Profile: m.Profile}
			groups[k] = g
			rows[k] = map[string]*platformRow{}
		}
		if _, sw := topSweepStep(r.RR); sw {
			g.IsSweep = true
		}
		row, ok := rows[k][m.Platform]
		if !ok {
			row = &platformRow{Platform: m.Platform}
			rows[k][m.Platform] = row
			g.Rows = append(g.Rows, row)
		}
		if len(r.Problems) == 0 {
			row.Valid = append(row.Valid, r)
		} else {
			row.Invalid = append(row.Invalid, r)
		}
	}

	var out []*comparisonGroup
	for _, g := range groups {
		for _, row := range g.Rows {
			aggregate(row)
		}
		sort.SliceStable(g.Rows, func(i, j int) bool {
			a, b := g.Rows[i], g.Rows[j]
			if (len(a.Valid) > 0) != (len(b.Valid) > 0) {
				return len(a.Valid) > 0
			}
			if a.MedConfTPS != b.MedConfTPS {
				return a.MedConfTPS > b.MedConfTPS
			}
			return a.Platform < b.Platform
		})
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Normalized != b.Normalized {
			return a.Normalized
		}
		if a.RunName != b.RunName {
			return a.RunName < b.RunName
		}
		if a.Workload != b.Workload {
			return a.Workload < b.Workload
		}
		return a.Profile < b.Profile
	})
	return out
}

func aggregate(row *platformRow) {
	var conf, p50, p99, fail, sat []float64
	seen := map[string]bool{}
	for _, r := range row.Valid {
		h := r.RR.Headline
		conf = append(conf, h.ConfirmedTPS)
		p50 = append(p50, pctl(h.E2E, "p50"))
		p99 = append(p99, pctl(h.E2E, "p99"))
		fail = append(fail, h.FailureRate)
		if r.RR.SaturationTPS > 0 {
			sat = append(sat, float64(r.RR.SaturationTPS))
		}
		for _, c := range r.RR.Manifest.Caveats {
			if !seen[c] {
				seen[c] = true
				row.Caveats = append(row.Caveats, c)
			}
		}
		m := r.RR.Manifest
		row.Crypto, row.PlatformVer = m.Crypto, m.PlatformVersion
		row.ResourceString = fmt.Sprintf("%.2g CPU / %.2g GB over %d containers", m.ResourceCPUsTotal, m.ResourceMemTotalGB, m.ResourceContainers)
	}
	if len(conf) == 0 {
		return
	}
	row.MedConfTPS, row.MedE2EP50, row.MedE2EP99 = median(conf), median(p50), median(p99)
	row.MedFailRate, row.MedSaturation = median(fail), median(sat)
	row.MinConfTPS, row.MaxConfTPS = conf[0], conf[0]
	for _, c := range conf {
		if c < row.MinConfTPS {
			row.MinConfTPS = c
		}
		if c > row.MaxConfTPS {
			row.MaxConfTPS = c
		}
	}
}

// BuildReport writes a cross-platform comparison of the runs under resultsDir.
//
// Only harness-level metrics appear, and only from runs that pass the rejection
// rules in docs/architecture/fairness-guarantees.md; rejected runs are listed
// with the reason instead of being averaged in. Normalized and platform-native
// runs are reported in separate sections and never ranked together.
func BuildReport(resultsDir, out string, opt ReportOptions) error {
	recs, err := loadRunRecords(resultsDir, opt)
	if err != nil {
		return err
	}
	return os.WriteFile(out, []byte(renderComparison(buildComparison(recs), recs, resultsDir, opt)), 0o644)
}

func renderComparison(groups []*comparisonGroup, recs []runRecord, resultsDir string, opt ReportOptions) string {
	esc := html.EscapeString
	valid := 0
	for _, r := range recs {
		if len(r.Problems) == 0 {
			valid++
		}
	}

	var b strings.Builder
	b.WriteString(`<title>Benchmark Comparison</title>
<style>
:root{--bg:#fafaf7;--fg:#1d1d1b;--mute:#6b6b66;--line:#dedcd4;--head:#efede6;--warn:#8a5a00;--bad:#a1261a;--good:#1f6b3a}
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#171716;--fg:#ecebe6;--mute:#9a9a93;--line:#34332f;--head:#23221f;--warn:#e0a93b;--bad:#f07b6c;--good:#6cc48a}}
:root[data-theme=dark]{--bg:#171716;--fg:#ecebe6;--mute:#9a9a93;--line:#34332f;--head:#23221f;--warn:#e0a93b;--bad:#f07b6c;--good:#6cc48a}
body{background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif;margin:0 auto;max-width:1100px;padding:2rem 1.2rem}
h1{font-size:1.5rem;margin:0 0 .3rem}h2{font-size:1.15rem;margin:2.2rem 0 .4rem;border-top:1px solid var(--line);padding-top:1rem}
h3{font-size:1rem;margin:1.6rem 0 .3rem}.mute{color:var(--mute)}.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums}
th,td{border-bottom:1px solid var(--line);padding:.35rem .55rem;text-align:right;white-space:nowrap}
th{background:var(--head);font-weight:600}td:first-child,th:first-child{text-align:left}
.warn{color:var(--warn)}.bad{color:var(--bad)}.good{color:var(--good)}
ul{margin:.3rem 0 .3rem 1.1rem;padding:0}li{margin:.1rem 0}details{margin:.4rem 0}
</style>
<h1>Cross-platform benchmark comparison</h1>
`)
	fmt.Fprintf(&b, `<p class=mute>Generated %s from <code>%s</code>`, time.Now().Format("2006-01-02 15:04"), esc(resultsDir))
	if !opt.Since.IsZero() {
		fmt.Fprintf(&b, `, runs since %s`, opt.Since.Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(&b, `. %d runs found, %d comparable, %d excluded.</p>
<p class=mute>Harness-level metrics only: confirmed TPS, end-to-end latency (scheduled send to observed commit), failure rate. Platform-native breakdowns are excluded by design. Each figure is the median over comparable replicates; the spread column is min&ndash;max.</p>
`, len(recs), valid, len(recs)-valid)

	section := func(norm bool, title, blurb string) {
		any := false
		for _, g := range groups {
			if g.Normalized == norm {
				any = true
				break
			}
		}
		if !any {
			return
		}
		fmt.Fprintf(&b, "<h2>%s</h2><p class=mute>%s</p>\n", title, blurb)
		for _, g := range groups {
			if g.Normalized != norm {
				continue
			}
			renderGroup(&b, g)
		}
	}
	section(true, "Normalized comparison",
		"Identical workload, key space, seed, windows, sweep ladder, orderer batch parameters and total resource budget on every platform. This is the head-to-head.")
	section(false, "Platform-native runs",
		"Each platform on its own tuned configuration. Shows what a platform can do, not how platforms compare &mdash; never rank these against each other or against the normalized section.")
	if len(groups) == 0 {
		b.WriteString("<p>No runs found.</p>")
	}
	return b.String()
}

func renderGroup(b *strings.Builder, g *comparisonGroup) {
	esc := html.EscapeString
	fmt.Fprintf(b, "<h3>%s &middot; %s <span class=mute>(profile %s)</span></h3>\n", esc(g.RunName), esc(g.Workload), esc(g.Profile))
	tpsLabel := "confirmed TPS"
	if g.IsSweep {
		tpsLabel = "confirmed TPS (hold)"
	}
	b.WriteString(`<div class=scroll><table><tr><th>platform`)
	fmt.Fprintf(b, "<th>%s<th>spread", tpsLabel)
	if g.IsSweep {
		b.WriteString("<th>saturation TPS")
	}
	b.WriteString("<th>e2e p50 ms<th>e2e p99 ms<th>fail rate<th>runs</tr>\n")

	for _, row := range g.Rows {
		n := len(row.Valid)
		if n == 0 {
			cols := 6
			if g.IsSweep {
				cols = 7
			}
			fmt.Fprintf(b, "<tr><td>%s<td colspan=%d class=bad style=text-align:left>no comparable run (%d excluded, see below)</tr>\n",
				esc(row.Platform), cols, len(row.Invalid))
			continue
		}
		spread := "&ndash;"
		if n > 1 {
			spread = fmt.Sprintf("%.0f&ndash;%.0f", row.MinConfTPS, row.MaxConfTPS)
		}
		fmt.Fprintf(b, "<tr><td>%s<td>%.1f<td>%s", esc(row.Platform), row.MedConfTPS, spread)
		if g.IsSweep {
			fmt.Fprintf(b, "<td>%.0f", row.MedSaturation)
		}
		runs := fmt.Sprintf("%d", n)
		if n == 1 {
			runs = `1 <span class=warn title="single run: no replicate, spread unknown">&#9888;</span>`
		}
		if len(row.Invalid) > 0 {
			runs += fmt.Sprintf(` <span class=mute>(+%d excluded)</span>`, len(row.Invalid))
		}
		fmt.Fprintf(b, "<td>%.1f<td>%.1f<td>%.4f<td>%s</tr>\n", row.MedE2EP50, row.MedE2EP99, row.MedFailRate, runs)
	}
	b.WriteString("</table></div>\n")

	// Caveats and crypto disclosure travel with the numbers (adr-010, and
	// "Disclosed, not equalized" in fairness-guarantees.md).
	var notes strings.Builder
	for _, row := range g.Rows {
		if len(row.Valid) == 0 {
			continue
		}
		fmt.Fprintf(&notes, "<li><b>%s</b> <span class=mute>%s &middot; %s &middot; sig %s, hash %s, per-tx endorsement verify %v</span>",
			esc(row.Platform), esc(row.PlatformVer), esc(row.ResourceString),
			esc(orDefault(row.Crypto.SignatureAlg, "?")), esc(orDefault(row.Crypto.HashAlg, "?")), row.Crypto.PerTxEndorsementVerify)
		if len(row.Caveats) > 0 {
			notes.WriteString("<ul>")
			for _, c := range row.Caveats {
				fmt.Fprintf(&notes, "<li class=warn>%s</li>", esc(c))
			}
			notes.WriteString("</ul>")
		}
		notes.WriteString("</li>")
	}
	if notes.Len() > 0 {
		fmt.Fprintf(b, "<ul>%s</ul>\n", notes.String())
	}

	var excluded strings.Builder
	count := 0
	for _, row := range g.Rows {
		for _, r := range row.Invalid {
			count++
			fmt.Fprintf(&excluded, "<li><b>%s</b> %s <span class=mute>%s</span><ul>",
				esc(row.Platform), r.RR.Manifest.StartedAt.Format("2006-01-02 15:04"), esc(r.Path))
			for _, p := range r.Problems {
				fmt.Fprintf(&excluded, "<li class=bad>%s</li>", esc(p))
			}
			excluded.WriteString("</ul></li>")
		}
	}
	if count > 0 {
		fmt.Fprintf(b, "<details><summary>%d excluded run(s) and why</summary><ul>%s</ul></details>\n", count, excluded.String())
	}
}

package harness

import (
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

// The run book is one self-contained HTML file with an overview of every run in
// a results tree and a page per run: status, headline figures, throughput and
// latency by phase, platform resource use, the full phase table, errors, the
// manifest, and links to the run's own files. Unlike the comparison report it
// aggregates nothing and hides nothing: failed and aborted runs get pages too,
// so the book is the record of what happened, not of what is comparable.
//
// It is rebuilt from the results tree after every run, so each run adds its page.

// RunbookFile is the run book's file name inside a results directory.
const RunbookFile = "index.html"

type runStatus struct {
	Key, Label, Icon string
}

var (
	statusCompleted = runStatus{"completed", "Completed", "✓"}
	statusExcluded  = runStatus{"excluded", "Completed, not comparable", "!"}
	statusFailed    = runStatus{"failed", "Platform failure", "✕"}
	statusAborted   = runStatus{"aborted", "Aborted before results", "–"}
)

type runbookEntry struct {
	ID        string
	Rel       string // run directory relative to the results root
	Link      string // run directory relative to the output file, slash-separated
	Manifest  Manifest
	RR        *RunResult
	ErrorText string
	Files     []string
	Problems  []string
	Status    runStatus
	started   time.Time
}

// BuildRunbook writes the run book for every run under resultsDir to out.
func BuildRunbook(resultsDir, out string) error {
	entries, empty, err := loadRunbookEntries(resultsDir, out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("run book: %w", err)
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, []byte(renderRunbook(entries, empty, time.Now())), 0o644); err != nil {
		return fmt.Errorf("write run book %s: %w", out, err)
	}
	return os.Rename(tmp, out)
}

func loadRunbookEntries(resultsDir, out string) ([]runbookEntry, int, error) {
	fi, err := os.Stat(resultsDir)
	if err != nil {
		return nil, 0, fmt.Errorf("results dir %s: %w", resultsDir, err)
	}
	if !fi.IsDir() {
		return nil, 0, fmt.Errorf("results dir %s is not a directory", resultsDir)
	}
	outDir, _ := filepath.Abs(filepath.Dir(out))
	var entries []runbookEntry
	empty := 0
	seen := map[string]int{}
	err = filepath.WalkDir(resultsDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			slog.Warn("run book: cannot read part of the results tree", "path", path, "err", werr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() || path == resultsDir {
			return nil
		}
		if strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir // campaign logs, captures
		}
		files := runFiles(path)
		has := func(n string) bool {
			for _, f := range files {
				if f == n {
					return true
				}
			}
			return false
		}
		if !has("result.json") && !has("manifest.json") && !has("error.txt") {
			if len(files) == 0 && isRunDirName(d.Name()) {
				empty++
			}
			return nil
		}
		e := runbookEntry{Files: files}
		e.Rel, _ = filepath.Rel(resultsDir, path)
		if abs, aerr := filepath.Abs(path); aerr == nil {
			if l, lerr := filepath.Rel(outDir, abs); lerr == nil {
				e.Link = filepath.ToSlash(l)
			}
		}
		if has("result.json") {
			var rr RunResult
			if b, rerr := os.ReadFile(filepath.Join(path, "result.json")); rerr != nil {
				slog.Warn("run book: unreadable result.json", "path", path, "err", rerr)
			} else if jerr := json.Unmarshal(b, &rr); jerr != nil {
				slog.Warn("run book: corrupt result.json", "path", path, "err", jerr)
			} else {
				e.RR = &rr
				e.Manifest = rr.Manifest
			}
		}
		if e.RR == nil && has("manifest.json") {
			if b, rerr := os.ReadFile(filepath.Join(path, "manifest.json")); rerr == nil {
				_ = json.Unmarshal(b, &e.Manifest)
			}
		}
		if has("error.txt") {
			if b, rerr := os.ReadFile(filepath.Join(path, "error.txt")); rerr == nil {
				e.ErrorText = strings.TrimSpace(string(b))
			}
		}
		if e.Manifest.Platform == "" {
			e.Manifest.Platform = filepath.Base(filepath.Dir(path))
		}
		e.started = e.Manifest.StartedAt
		if e.started.IsZero() {
			if t, perr := time.ParseInLocation("20060102-150405", d.Name(), time.Local); perr == nil {
				e.started = t
			}
		}
		e.Status, e.Problems = classifyRun(e)
		id := slug(e.Rel)
		if n := seen[id]; n > 0 {
			id = fmt.Sprintf("%s-%d", id, n+1)
		}
		seen[slug(e.Rel)]++
		e.ID = id
		entries = append(entries, e)
		return filepath.SkipDir
	})
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].started.Equal(entries[j].started) {
			return entries[i].started.After(entries[j].started)
		}
		return entries[i].Rel < entries[j].Rel
	})
	return entries, empty, err
}

func runFiles(dir string) []string {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	sort.Strings(out)
	return out
}

func isRunDirName(s string) bool {
	_, err := time.Parse("20060102-150405", s)
	return err == nil
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.ToSlash(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func classifyRun(e runbookEntry) (runStatus, []string) {
	if len(e.Manifest.ContainerFailures) > 0 {
		var p []string
		if e.RR != nil {
			p = runProblems(*e.RR)
		}
		return statusFailed, p
	}
	if e.RR == nil {
		return statusAborted, nil
	}
	if p := runProblems(*e.RR); len(p) > 0 {
		return statusExcluded, p
	}
	return statusCompleted, nil
}

// ---- figures ----------------------------------------------------------------

func (e runbookEntry) peakConfirmed() (float64, string) {
	best, name := 0.0, ""
	if e.RR == nil {
		return 0, ""
	}
	for _, ph := range e.RR.Phases {
		if ph.Result.ConfirmedTPS > best {
			best, name = ph.Result.ConfirmedTPS, ph.Name
		}
	}
	return best, name
}

func (e runbookEntry) totals() (submitted, committed int64) {
	if e.RR == nil {
		return 0, 0
	}
	for _, ph := range e.RR.Phases {
		submitted += ph.Result.Submitted
		committed += ph.Result.Committed
	}
	return
}

func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func fmtNum(v float64, dec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "–"
	}
	whole := math.Trunc(v)
	s := fmtInt(int64(whole))
	if dec <= 0 {
		return fmtInt(int64(math.Round(v)))
	}
	frac := strconv.FormatFloat(math.Abs(v-whole), 'f', dec, 64)
	if strings.HasPrefix(frac, "1") { // rounding carried into the whole part
		return fmtNum(math.Round(v*math.Pow10(dec))/math.Pow10(dec), dec)
	}
	if v < 0 && whole == 0 {
		s = "-0"
	}
	return s + frac[1:]
}

// compact renders 1284 as "1.3k" and 10000 as "10k", for axis and tile labels.
func compact(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e6:
		return strings.TrimSuffix(strconv.FormatFloat(v/1e6, 'f', 1, 64), ".0") + "M"
	case a >= 1e3:
		return strings.TrimSuffix(strconv.FormatFloat(v/1e3, 'f', 1, 64), ".0") + "k"
	case a >= 100 || v == math.Trunc(v):
		return strconv.FormatFloat(v, 'f', 0, 64)
	default:
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
}

func fmtPct(v float64) string {
	switch {
	case v == 0:
		return "0%"
	case v < 0.001:
		return strconv.FormatFloat(v*100, 'f', 3, 64) + "%"
	case v < 0.1:
		return strconv.FormatFloat(v*100, 'f', 2, 64) + "%"
	default:
		return strconv.FormatFloat(v*100, 'f', 1, 64) + "%"
	}
}

func fmtSpan(d time.Duration) string {
	if d <= 0 {
		return "–"
	}
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "–"
	}
	return t.Format("2006-01-02 15:04:05")
}

// phaseAxisLabel shortens "sweep-1000" to "1k"; other phase names stay as they are.
func phaseAxisLabel(ph PhaseResult) string {
	if strings.HasPrefix(ph.Name, "sweep-") {
		return compact(float64(ph.OfferedTPS))
	}
	return ph.Name
}

// ---- charts -----------------------------------------------------------------

type chartSeries struct {
	Name   string
	Slot   int       // categorical slot, 1-based
	Values []float64 // NaN = no value at that x
}

type chartTip struct {
	Title string      `json:"t"`
	Rows  [][3]string `json:"r"` // value, label, slot
}

// niceTicks returns rounded tick values covering [0, max].
func niceTicks(max float64, want int) []float64 {
	if max <= 0 || math.IsNaN(max) {
		return []float64{0, 1}
	}
	raw := max / float64(want)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	var ticks []float64
	for v := 0.0; v < max+step*0.999; v += step {
		ticks = append(ticks, v)
	}
	return ticks
}

// lineChart renders an inline SVG line chart with one shared y axis, a hover
// column per x, and the tooltip payload on each column. unit follows values in
// the tooltip; xTitles are the tooltip headings.
func lineChart(title, unit string, xLabels, xTitles []string, series []chartSeries, labelEvery int) string {
	const w, h = 520.0, 240.0
	const ml, mr, mt, mb = 48.0, 12.0, 12.0, 30.0
	pw, ph := w-ml-mr, h-mt-mb
	n := len(xLabels)
	if n == 0 {
		return ""
	}
	max := 0.0
	for _, s := range series {
		for _, v := range s.Values {
			if !math.IsNaN(v) && v > max {
				max = v
			}
		}
	}
	ticks := niceTicks(max, 4)
	top := ticks[len(ticks)-1]
	x := func(i int) float64 {
		if n == 1 {
			return ml + pw/2
		}
		return ml + pw*float64(i)/float64(n-1)
	}
	y := func(v float64) float64 { return mt + ph - ph*v/top }
	esc := html.EscapeString

	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="chart"><figcaption>%s</figcaption>`, esc(title))
	if len(series) > 1 {
		b.WriteString(`<div class="legend">`)
		for _, s := range series {
			fmt.Fprintf(&b, `<span><i class="key s%d"></i>%s</span>`, s.Slot, esc(s.Name))
		}
		b.WriteString(`</div>`)
	}
	fmt.Fprintf(&b, `<div class="plot"><svg viewBox="0 0 %g %g" role="img" aria-label="%s">`, w, h, esc(title))
	for _, t := range ticks {
		fmt.Fprintf(&b, `<line class="grid" x1="%g" x2="%g" y1="%.1f" y2="%.1f"/>`, ml, w-mr, y(t), y(t))
		fmt.Fprintf(&b, `<text class="tick" x="%g" y="%.1f" text-anchor="end" dominant-baseline="middle">%s</text>`, ml-8, y(t), esc(compact(t)))
	}
	fmt.Fprintf(&b, `<line class="base" x1="%g" x2="%g" y1="%g" y2="%g"/>`, ml, w-mr, mt+ph, mt+ph)
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i, l := range xLabels {
		if i != n-1 && (i%labelEvery != 0 || (n-1-i) < labelEvery) {
			continue // off-step, or close enough to the last label to collide
		}
		anchor := "middle"
		if n > 1 && i == 0 {
			anchor = "start"
		} else if n > 1 && i == n-1 {
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%g" text-anchor="%s">%s</text>`, x(i), h-10, anchor, esc(l))
	}
	for _, s := range series {
		var d strings.Builder
		pen := false
		for i, v := range s.Values {
			if math.IsNaN(v) {
				pen = false
				continue
			}
			if pen {
				fmt.Fprintf(&d, "L%.1f %.1f", x(i), y(v))
			} else {
				fmt.Fprintf(&d, "M%.1f %.1f", x(i), y(v))
				pen = true
			}
		}
		fmt.Fprintf(&b, `<path class="line s%d" d="%s"/>`, s.Slot, d.String())
		if n <= 40 {
			for i, v := range s.Values {
				if !math.IsNaN(v) {
					fmt.Fprintf(&b, `<circle class="dot s%d" cx="%.1f" cy="%.1f" r="4"/>`, s.Slot, x(i), y(v))
				}
			}
		}
	}
	fmt.Fprintf(&b, `<line class="cross" x1="0" x2="0" y1="%g" y2="%g"/>`, mt, mt+ph)
	band := pw
	if n > 1 {
		band = pw / float64(n-1)
	}
	for i := 0; i < n; i++ {
		tip := chartTip{Title: xTitles[i]}
		for _, s := range series {
			val := "no value"
			if v := s.Values[i]; !math.IsNaN(v) {
				val = fmtNum(v, decimalsFor(v)) + unit
			}
			tip.Rows = append(tip.Rows, [3]string{val, s.Name, strconv.Itoa(s.Slot)})
		}
		js, _ := json.Marshal(tip)
		left := math.Max(ml, x(i)-band/2)
		right := math.Min(w-mr, x(i)+band/2)
		fmt.Fprintf(&b, `<rect class="hit" x="%.1f" y="%g" width="%.1f" height="%g" data-x="%.1f" data-tip="%s"/>`,
			left, mt, right-left, ph, x(i), esc(string(js)))
	}
	b.WriteString(`</svg></div></figure>`)
	return b.String()
}

func decimalsFor(v float64) int {
	switch a := math.Abs(v); {
	case a >= 100:
		return 0
	case a >= 1:
		return 1
	default:
		return 2
	}
}

func phaseCharts(rr *RunResult) string {
	if rr == nil || len(rr.Phases) == 0 {
		return ""
	}
	n := len(rr.Phases)
	labels, titles := make([]string, n), make([]string, n)
	offered, confirmed := make([]float64, n), make([]float64, n)
	p50, p99 := make([]float64, n), make([]float64, n)
	for i, ph := range rr.Phases {
		labels[i] = phaseAxisLabel(ph)
		titles[i] = fmt.Sprintf("%s · target %s TPS", ph.Name, fmtInt(int64(ph.OfferedTPS)))
		offered[i] = float64(ph.OfferedTPS)
		confirmed[i] = ph.Result.ConfirmedTPS
		if ph.Result.Committed > 0 {
			p50[i], p99[i] = pctl(ph.Result.E2E, "p50"), pctl(ph.Result.E2E, "p99")
		} else {
			p50[i], p99[i] = math.NaN(), math.NaN()
		}
	}
	every := 1
	if n > 12 {
		every = (n + 11) / 12
	}
	return `<div class="charts">` +
		lineChart("Throughput by phase (TPS)", " TPS", labels, titles, []chartSeries{
			{Name: "Target", Slot: 1, Values: offered},
			{Name: "Confirmed", Slot: 2, Values: confirmed},
		}, every) +
		lineChart("End-to-end latency by phase (ms)", " ms", labels, titles, []chartSeries{
			{Name: "p50", Slot: 1, Values: p50},
			{Name: "p99", Slot: 2, Values: p99},
		}, every) +
		`</div>`
}

const resourceChartSlots = 12

func resourceCharts(rr *RunResult) string {
	if rr == nil || len(rr.SystemSamples) < 2 {
		return ""
	}
	ss := rr.SystemSamples
	step := 1
	if len(ss) > 240 {
		step = (len(ss) + 239) / 240
	}
	t0 := ss[0].T
	var labels, titles []string
	var picked []metrics.SystemSample
	for i := 0; i < len(ss); i += step {
		s := ss[i]
		el := s.T.Sub(t0).Round(time.Second)
		labels = append(labels, fmtElapsed(el))
		titles = append(titles, fmt.Sprintf("%s (+%s) · %d containers", s.T.Format("15:04:05"), fmtElapsed(el), s.Containers))
		picked = append(picked, s)
	}
	cpu, mem := resourceSeries(picked)
	cpuTitle, memTitle := "Platform CPU (cores; 1.0 = one full core)", "Platform memory (MiB)"
	if len(metrics.SampledContainerNames(picked)) > 0 {
		cpuTitle, memTitle = "Platform CPU by container (cores; 1.0 = one full core)", "Platform memory by container (MiB)"
	}
	every := (len(labels) + 7) / 8
	return `<div class="charts">` +
		lineChart(cpuTitle, " cores", labels, titles, cpu, every) +
		lineChart(memTitle, " MiB", labels, titles, mem, every) +
		`</div>`
}

func resourceSeries(samples []metrics.SystemSample) (cpu, mem []chartSeries) {
	names := metrics.SampledContainerNames(samples)
	n := len(samples)
	if len(names) == 0 {
		cpuVals, memVals := make([]float64, n), make([]float64, n)
		for i, s := range samples {
			cpuVals[i] = s.CPUPercent / 100 // docker stats: 100 = one core
			memVals[i] = float64(s.MemBytes) / (1 << 20)
		}
		return []chartSeries{{Name: "all containers", Slot: 1, Values: cpuVals}},
			[]chartSeries{{Name: "all containers", Slot: 1, Values: memVals}}
	}
	cpu = make([]chartSeries, len(names))
	mem = make([]chartSeries, len(names))
	for i, name := range names {
		slot := (i % resourceChartSlots) + 1
		label := metrics.ShortContainerName(name)
		cpu[i] = chartSeries{Name: label, Slot: slot, Values: make([]float64, n)}
		mem[i] = chartSeries{Name: label, Slot: slot, Values: make([]float64, n)}
		for j := range samples {
			cpu[i].Values[j] = math.NaN()
			mem[i].Values[j] = math.NaN()
		}
	}
	index := make(map[string]int, len(names))
	for i, name := range names {
		index[name] = i
	}
	for j, s := range samples {
		for _, u := range s.ByContainer {
			i, ok := index[u.Name]
			if !ok {
				continue
			}
			cpu[i].Values[j] = u.CPUPercent / 100 // docker stats: 100 = one core
			mem[i].Values[j] = float64(u.MemBytes) / (1 << 20)
		}
	}
	return cpu, mem
}

func fmtElapsed(d time.Duration) string {
	m, s := int(d.Minutes()), int(d.Seconds())%60
	return fmt.Sprintf("%d:%02d", m, s)
}

// ---- page -------------------------------------------------------------------

func renderRunbook(entries []runbookEntry, empty int, now time.Time) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(runbookHead)

	counts := map[string]int{}
	platforms := map[string]int{}
	var platformOrder []string
	for _, e := range entries {
		counts[e.Status.Key]++
		if platforms[e.Manifest.Platform] == 0 {
			platformOrder = append(platformOrder, e.Manifest.Platform)
		}
		platforms[e.Manifest.Platform]++
	}
	sort.Strings(platformOrder)

	// Sidebar.
	b.WriteString(`<div class="shell"><nav class="side" aria-label="Runs"><a class="brand" href="#overview">Benchmark runs</a>`)
	b.WriteString(`<button class="navtoggle" type="button" aria-expanded="false">Browse runs</button><div class="navbody">`)
	b.WriteString(`<input class="search" type="search" placeholder="Filter runs" aria-label="Filter runs">`)
	for _, p := range platformOrder {
		fmt.Fprintf(&b, `<div class="group"><div class="group-h">%s <span class="mute">%d</span></div><ul>`, esc(p), platforms[p])
		for _, e := range entries {
			if e.Manifest.Platform != p {
				continue
			}
			name := e.Manifest.RunName
			if name == "" {
				name = "unnamed run"
			}
			fmt.Fprintf(&b, `<li data-run="%s" data-status="%s" data-platform="%s" data-text="%s"><a href="#%s"><span class="st %s" title="%s">%s</span><span class="nm">%s</span><span class="when">%s</span></a></li>`,
				esc(e.ID), e.Status.Key, esc(p), esc(strings.ToLower(p+" "+name+" "+e.Manifest.Profile+" "+e.Rel)), esc(e.ID),
				e.Status.Key, esc(e.Status.Label), e.Status.Icon, esc(name), esc(shortWhen(e.started)))
		}
		b.WriteString(`</ul></div>`)
	}
	b.WriteString(`</div></nav><main>`)

	// Overview.
	b.WriteString(`<section class="page" id="overview"><header class="page-h"><h1>Benchmark runs</h1>`)
	fmt.Fprintf(&b, `<p class="mute">Generated %s · %d runs across %d platforms`, esc(now.Format("2006-01-02 15:04")), len(entries), len(platformOrder))
	if empty > 0 {
		fmt.Fprintf(&b, ` · %d empty run folder(s) not listed`, empty)
	}
	b.WriteString(`</p></header><div class="tiles">`)
	tile(&b, "Runs", fmtInt(int64(len(entries))), "")
	tile(&b, "Completed", fmtInt(int64(counts["completed"]+counts["excluded"])), fmt.Sprintf("%d not comparable", counts["excluded"]))
	tile(&b, "Platform failures", fmtInt(int64(counts["failed"])), "a container exited or was OOM-killed")
	tile(&b, "Aborted before results", fmtInt(int64(counts["aborted"])), "no result.json written")
	b.WriteString(`</div><div class="filters"><label>Platform <select data-filter="platform"><option value="">All</option>`)
	for _, p := range platformOrder {
		fmt.Fprintf(&b, `<option>%s</option>`, esc(p))
	}
	b.WriteString(`</select></label><label>Status <select data-filter="status"><option value="">All</option>`)
	for _, s := range []runStatus{statusCompleted, statusExcluded, statusFailed, statusAborted} {
		fmt.Fprintf(&b, `<option value="%s">%s</option>`, s.Key, esc(s.Label))
	}
	b.WriteString(`</select></label></div><div class="scroll"><table class="runs"><thead><tr><th>Started</th><th>Platform</th><th>Run</th><th>Profile</th><th>Status</th><th class="num">Saturation TPS</th><th class="num">Peak confirmed TPS</th><th class="num">Headline p99</th><th class="num">Headline failures</th></tr></thead><tbody>`)
	for _, e := range entries {
		sat, peak, p99, fail := "–", "–", "–", "–"
		if e.RR != nil {
			if e.RR.SaturationTPS > 0 {
				sat = fmtInt(int64(e.RR.SaturationTPS))
			}
			if v, _ := e.peakConfirmed(); v > 0 {
				peak = fmtNum(v, 1)
			}
			if h := e.RR.Headline; h != nil && h.Committed > 0 {
				p99 = fmtNum(pctl(h.E2E, "p99"), 1) + " ms"
				fail = fmtPct(h.FailureRate)
			} else if h != nil {
				fail = fmtPct(h.FailureRate)
			}
		}
		fmt.Fprintf(&b, `<tr data-run="%s" data-status="%s" data-platform="%s"><td><a href="#%s">%s</a></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td></tr>`,
			esc(e.ID), e.Status.Key, esc(e.Manifest.Platform), esc(e.ID), esc(fmtTime(e.started)), esc(e.Manifest.Platform), esc(orDash(e.Manifest.RunName)),
			esc(orDash(e.Manifest.Profile)), statusBadge(e.Status), sat, peak, p99, fail)
	}
	b.WriteString(`</tbody></table></div></section>`)

	for i, e := range entries {
		var prev, next *runbookEntry
		if i > 0 {
			prev = &entries[i-1]
		}
		if i+1 < len(entries) {
			next = &entries[i+1]
		}
		renderRunPage(&b, e, prev, next)
	}
	b.WriteString(`</main></div><div class="tooltip" role="tooltip" hidden></div>`)
	b.WriteString(runbookScript)
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "–"
	}
	return s
}

func shortWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("Jan 02 15:04")
}

func statusBadge(s runStatus) string {
	return fmt.Sprintf(`<span class="badge %s"><span aria-hidden="true">%s</span> %s</span>`, s.Key, s.Icon, html.EscapeString(s.Label))
}

func tile(b *strings.Builder, label, value, note string) {
	fmt.Fprintf(b, `<div class="tile"><div class="tile-l">%s</div><div class="tile-v">%s</div>`, html.EscapeString(label), html.EscapeString(value))
	if note != "" {
		fmt.Fprintf(b, `<div class="tile-n">%s</div>`, html.EscapeString(note))
	}
	b.WriteString(`</div>`)
}

func renderRunPage(b *strings.Builder, e runbookEntry, prev, next *runbookEntry) {
	esc := html.EscapeString
	m := e.Manifest
	name := orDash(m.RunName)
	fmt.Fprintf(b, `<section class="page run" id="%s"><header class="page-h">`, esc(e.ID))
	b.WriteString(`<div class="crumbs"><a href="#overview">All runs</a><span class="pager">`)
	if prev != nil {
		fmt.Fprintf(b, `<a href="#%s" title="%s">← Newer</a>`, esc(prev.ID), esc(prev.Rel))
	}
	if next != nil {
		fmt.Fprintf(b, `<a href="#%s" title="%s">Older →</a>`, esc(next.ID), esc(next.Rel))
	}
	b.WriteString(`</span></div>`)
	fmt.Fprintf(b, `<h1>%s <span class="mute">·</span> %s</h1>`, esc(m.Platform), esc(name))
	fmt.Fprintf(b, `<p class="meta">%s <span>%s</span> <span>%s</span> <span>profile %s</span> <span><code>%s</code></span></p></header>`,
		statusBadge(e.Status), esc(fmtTime(e.started)), esc(orDash(m.Workload)), esc(orDash(m.Profile)), esc(filepath.ToSlash(e.Rel)))

	if len(m.ContainerFailures) > 0 {
		b.WriteString(`<div class="callout failed"><strong>✕ Platform failure.</strong> Load results after this point measure a broken network.<ul>`)
		for _, f := range m.ContainerFailures {
			fmt.Fprintf(b, `<li>%s</li>`, esc(f.String()))
		}
		b.WriteString(`</ul></div>`)
	}
	if e.RR == nil {
		b.WriteString(`<div class="callout aborted"><strong>– No results.</strong> The run stopped before writing result.json.`)
		if e.ErrorText != "" {
			fmt.Fprintf(b, `<pre>%s</pre>`, esc(e.ErrorText))
		} else {
			b.WriteString(` No error.txt was written either; see the run's log if it has one.`)
		}
		b.WriteString(`</div>`)
	} else if e.ErrorText != "" {
		fmt.Fprintf(b, `<div class="callout failed"><strong>Run error</strong><pre>%s</pre></div>`, esc(e.ErrorText))
	}
	if len(e.Problems) > 0 && e.Status != statusFailed {
		b.WriteString(`<div class="callout excluded"><strong>! Not comparable.</strong> The comparison report excludes this run:<ul>`)
		for _, p := range e.Problems {
			fmt.Fprintf(b, `<li>%s</li>`, esc(p))
		}
		b.WriteString(`</ul></div>`)
	}

	if rr := e.RR; rr != nil {
		b.WriteString(`<div class="tiles">`)
		if rr.SaturationTPS > 0 {
			tile(b, "Saturation", fmtInt(int64(rr.SaturationTPS))+" TPS", "highest sweep step that held")
		} else {
			tile(b, "Saturation", "–", "no sweep step held, or not a sweep")
		}
		peak, at := e.peakConfirmed()
		tile(b, "Peak confirmed", fmtNum(peak, 1)+" TPS", at)
		if h := rr.Headline; h != nil && h.Committed > 0 {
			tile(b, "Headline latency p99", fmtNum(pctl(h.E2E, "p99"), 1)+" ms", "p50 "+fmtNum(pctl(h.E2E, "p50"), 1)+" ms")
		} else {
			tile(b, "Headline latency p99", "–", "headline committed nothing")
		}
		sub, com := e.totals()
		note := ""
		if sub > 0 {
			note = fmtPct(1-float64(com)/float64(sub)) + " not committed"
		}
		tile(b, "Committed", fmtInt(com), "of "+fmtInt(sub)+" submitted · "+note)
		tile(b, "Duration", fmtSpan(m.EndedAt.Sub(m.StartedAt)), fmt.Sprintf("%d phases", len(rr.Phases)))
		b.WriteString(`</div>`)

		b.WriteString(`<h2>Throughput and latency</h2>`)
		b.WriteString(phaseCharts(rr))
		if rc := resourceCharts(rr); rc != "" {
			b.WriteString(`<h2>Platform resources</h2>`)
			b.WriteString(rc)
		}

		b.WriteString(`<h2>Phases</h2><div class="scroll"><table><thead><tr><th>Phase</th><th class="num">Target TPS</th><th class="num">Offered TPS</th><th class="num">Confirmed TPS</th><th class="num">Submitted</th><th class="num">Committed</th><th class="num">Invalid</th><th class="num">Errored</th><th class="num">Timed out</th><th class="num">Failure rate</th><th class="num">e2e p50</th><th class="num">e2e p95</th><th class="num">e2e p99</th><th class="num">Send gap p99</th><th>Verdict</th></tr></thead><tbody>`)
		for _, ph := range rr.Phases {
			r := ph.Result
			lat := func(k string) string {
				if r.Committed == 0 {
					return "–"
				}
				return fmtNum(pctl(r.E2E, k), 1) + " ms"
			}
			vclass := ""
			switch {
			case ph.Verdict == "held":
				vclass = "good"
			case ph.Verdict != "":
				vclass = "warn"
			}
			fmt.Fprintf(b, `<tr><td>%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="num">%s</td><td class="%s">%s</td></tr>`,
				esc(ph.Name), fmtInt(int64(ph.OfferedTPS)), fmtNum(r.OfferedTPS, 1), fmtNum(r.ConfirmedTPS, 1), fmtInt(r.Submitted), fmtInt(r.Committed),
				fmtInt(r.Invalid), fmtInt(r.Errored), fmtInt(r.TimedOut), fmtPct(r.FailureRate), lat("p50"), lat("p95"), lat("p99"),
				fmtNum(pctl(r.SendGap, "p99"), 1)+" ms", vclass, esc(orDash(ph.Verdict)))
		}
		b.WriteString(`</tbody></table></div>`)

		var errRows strings.Builder
		for _, ph := range rr.Phases {
			for _, ec := range ph.Result.Errors {
				fmt.Fprintf(&errRows, `<tr><td>%s</td><td class="num">%s</td><td class="wrap">%s</td></tr>`, esc(ph.Name), fmtInt(ec.Count), esc(ec.Message))
			}
		}
		if errRows.Len() > 0 {
			b.WriteString(`<h2>Errors</h2><div class="scroll"><table><thead><tr><th>Phase</th><th class="num">Transactions</th><th>Message</th></tr></thead><tbody>`)
			b.WriteString(errRows.String())
			b.WriteString(`</tbody></table></div>`)
		}
	}

	if len(m.Caveats) > 0 {
		b.WriteString(`<h2>Caveats</h2><ul class="caveats">`)
		for _, c := range m.Caveats {
			fmt.Fprintf(b, `<li>%s</li>`, esc(c))
		}
		b.WriteString(`</ul>`)
	}

	if m.RunName != "" || m.StartedAt.IsZero() == false {
		b.WriteString(`<h2>Configuration</h2><div class="details">`)
		dl(b, "Run", [][2]string{
			{"Platform", m.Platform},
			{"Platform version", m.PlatformVersion},
			{"Run config", m.RunName},
			{"Workload", m.Workload},
			{"Normalized", yesNo(m.Normalized)},
			{"Profile", m.Profile},
			{"Started", fmtTime(m.StartedAt)},
			{"Ended", fmtTime(m.EndedAt)},
			{"Harness commit", m.HarnessGitSHA},
		})
		dist := m.KeyDistribution
		if m.ZipfianConstant > 0 {
			dist += fmt.Sprintf(" (s=%g)", m.ZipfianConstant)
		}
		skipped := ""
		for i, s := range m.SkippedSteps {
			if i > 0 {
				skipped += ", "
			}
			skipped += fmtInt(int64(s))
		}
		dl(b, "Load", [][2]string{
			{"Generators", strconv.Itoa(m.Generators)},
			{"Seed", strconv.FormatInt(m.Seed, 10)},
			{"Key space", fmtInt(int64(m.KeySpace))},
			{"Key distribution", dist},
			{"Read ratio", fmtPct(m.ReadWriteRatio)},
			{"Value size", fmtInt(int64(m.ValueSizeBytes)) + " bytes"},
			{"Warmup / cooldown", fmt.Sprintf("%gs / %gs", m.WarmupSec, m.CooldownSec)},
			{"Skipped sweep steps", skipped},
		})
		stateDB := m.StateDB
		if m.StateDBRequested != "" && m.StateDBRequested != m.StateDB {
			stateDB = fmt.Sprintf("%s (requested %s)", orDash(m.StateDB), m.StateDBRequested)
		}
		batch := ""
		if ob := m.OrdererBatch; ob.MaxMessageCount > 0 || ob.BatchTimeout != "" {
			batch = fmt.Sprintf("%d msgs, %s, preferred %s, max %s", ob.MaxMessageCount, orDash(ob.BatchTimeout), orDash(ob.PreferredMaxBytes), orDash(ob.AbsoluteMaxBytes))
		}
		dl(b, "Fairness levers", [][2]string{
			{"State DB", stateDB},
			{"Orderer batch", batch},
			{"Signature", m.Crypto.SignatureAlg},
			{"Hash", m.Crypto.HashAlg},
			{"Per-tx endorsement verify", yesNo(m.Crypto.PerTxEndorsementVerify)},
			{"Crypto note", m.Crypto.MSPNote},
		})
		var nodes []string
		for k, v := range m.Nodes {
			nodes = append(nodes, fmt.Sprintf("%s ×%d", k, v))
		}
		sort.Strings(nodes)
		perCPU := ""
		if m.ResourceLimit.CPUs > 0 {
			perCPU = fmt.Sprintf("%.2f", m.ResourceLimit.CPUs)
		}
		dl(b, "Resources", [][2]string{
			{"Total budget", fmt.Sprintf("%g CPU / %g GB", m.ResourceCPUsTotal, m.ResourceMemTotalGB)},
			{"Containers", strconv.Itoa(m.ResourceContainers)},
			{"CPU per container", perCPU},
			{"Memory weights", m.ResourceMemoryWeights},
			{"Nodes", strings.Join(nodes, ", ")},
		})
		b.WriteString(`</div>`)
		if len(m.ResourceMemory) > 0 {
			var names []string
			for k := range m.ResourceMemory {
				names = append(names, k)
			}
			sort.Strings(names)
			b.WriteString(`<details><summary>Memory limit per container</summary><div class="scroll"><table><thead><tr><th>Container</th><th class="num">Memory limit</th></tr></thead><tbody>`)
			for _, k := range names {
				fmt.Fprintf(b, `<tr><td>%s</td><td class="num">%s</td></tr>`, esc(k), esc(m.ResourceMemory[k]))
			}
			b.WriteString(`</tbody></table></div></details>`)
		}
	}

	if len(e.Files) > 0 {
		b.WriteString(`<h2>Files</h2><ul class="files">`)
		for _, f := range e.Files {
			href := e.Link + "/" + f
			fmt.Fprintf(b, `<li><a href="%s">%s</a></li>`, esc(href), esc(f))
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</section>`)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func dl(b *strings.Builder, title string, rows [][2]string) {
	fmt.Fprintf(b, `<div class="card"><h3>%s</h3><dl>`, html.EscapeString(title))
	for _, r := range rows {
		if strings.TrimSpace(r[1]) == "" || r[1] == "0" {
			continue
		}
		fmt.Fprintf(b, `<dt>%s</dt><dd>%s</dd>`, html.EscapeString(r[0]), html.EscapeString(r[1]))
	}
	b.WriteString(`</dl></div>`)
}

// IsRunbookDisabled reports whether BENCH_RUNBOOK=0 turned off the automatic
// rebuild after runs.
func IsRunbookDisabled() bool { return os.Getenv("BENCH_RUNBOOK") == "0" }

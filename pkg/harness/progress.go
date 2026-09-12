package harness

import (
	"fmt"
	"io"
	"time"

	"github.com/juicedcore/bench/pkg/metrics"
)

// progressReporter prints a live-updating summary of the currently running
// phase. In tty mode it redraws one line in place; otherwise it prints one
// plain line per tick so redirected output (a log file, --progress on a pipe)
// stays readable instead of filling up with carriage returns.
type progressReporter struct {
	w   io.Writer
	tty bool

	stopCh chan struct{}
	doneCh chan struct{}
}

func newProgressReporter(w io.Writer, tty bool) *progressReporter {
	return &progressReporter{w: w, tty: tty}
}

// start begins ticking once per second, reporting the collector's live
// aggregate over [phaseStart, now) until stop is called.
func (p *progressReporter) start(name string, targetTPS int, dur time.Duration, phaseStart time.Time, collector *metrics.Collector) {
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})

	go func() {
		defer close(p.doneCh)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case now := <-ticker.C:
				p.render(name, targetTPS, dur, phaseStart, now, collector)
			}
		}
	}()
}

func (p *progressReporter) render(name string, targetTPS int, dur time.Duration, phaseStart, now time.Time, collector *metrics.Collector) {
	elapsed := now.Sub(phaseStart)
	res := collector.Aggregate(metrics.Window{Start: phaseStart, End: now})
	p99 := res.E2E.Percentiles["p99"]

	line := fmt.Sprintf("[%s] %s/%s  target %d tps  offered %.0f tps  confirmed %.0f tps  p99 %.1fms  fail %.2f%%",
		name, fmtDur(elapsed), fmtDur(dur), targetTPS, res.OfferedTPS, res.ConfirmedTPS, p99, res.FailureRate*100)

	if p.tty {
		fmt.Fprintf(p.w, "\r\x1b[2K%s", line)
	} else {
		fmt.Fprintln(p.w, line)
	}
}

// stop ends the ticker goroutine and, in tty mode, moves past the redrawn
// line so subsequent output doesn't overwrite it.
func (p *progressReporter) stop() {
	close(p.stopCh)
	<-p.doneCh
	if p.tty {
		fmt.Fprintln(p.w)
	}
}

// fmtDur renders a duration as MM:SS (or HH:MM:SS past an hour), which is
// enough precision for a per-second-ticking progress line.
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second) / time.Second)
	h, rem := total/3600, total%3600
	m, s := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

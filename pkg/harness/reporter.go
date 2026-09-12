package harness

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/juicedcore/bench/pkg/metrics"
)

func writeSummaryText(path string, cfg *RunConfig, rr *RunResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run:        %s\n", cfg.Name)
	fmt.Fprintf(&b, "platform:   %s  (version %s)\n", cfg.Platform, rr.Manifest.PlatformVersion)
	fmt.Fprintf(&b, "workload:   %s   normalized=%v\n", cfg.Workload, cfg.Normalized)
	// Show both when the platform could not honour the requested state DB, so the
	// summary cannot be read as claiming a parity the run does not have.
	if rr.Manifest.StateDBRequested != "" && rr.Manifest.StateDB != rr.Manifest.StateDBRequested {
		fmt.Fprintf(&b, "profile:    %s   state_db=%s (requested %s - PARITY NOT HELD)\n",
			cfg.Profile, rr.Manifest.StateDB, rr.Manifest.StateDBRequested)
	} else {
		fmt.Fprintf(&b, "profile:    %s   state_db=%s\n", cfg.Profile, rr.Manifest.StateDB)
	}
	if m := rr.Manifest; m.ResourceContainers > 0 {
		fmt.Fprintf(&b, "resources:  %.2g CPU / %.2g GB total over %d containers (%.2f CPU / %s each)\n",
			m.ResourceCPUsTotal, m.ResourceMemTotalGB, m.ResourceContainers, m.ResourceLimit.CPUs, m.ResourceLimit.Memory)
	} else {
		fmt.Fprintf(&b, "resources:  NOT REPORTED - limits unverified\n")
	}
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
	fmt.Fprintf(&b, "%-12s %10s %12s %8s %10s %10s %10s %10s %10s  %s\n",
		"phase", "offered", "conf_tps", "goodput", "fail_rate", "e2e_p50", "e2e_p99", "gap_p99", "com_p50", "verdict")
	for _, ph := range rr.Phases {
		r := ph.Result
		fmt.Fprintf(&b, "%-12s %10d %12.1f %7.0f%% %10.4f %10.2f %10.2f %10.2f %10.2f  %s\n",
			ph.Name, ph.OfferedTPS, r.ConfirmedTPS, goodput(ph)*100, r.FailureRate,
			pctl(r.E2E, "p50"), pctl(r.E2E, "p99"), pctl(r.SendGap, "p99"), pctl(r.Commit, "p50"), ph.Verdict)
	}
	b.WriteString("\n")
	if len(rr.Manifest.SkippedSteps) > 0 {
		fmt.Fprintf(&b, "sweep aborted early; steps never offered: %v\n", rr.Manifest.SkippedSteps)
	}
	if n := len(cfg.Load.Sweep.Steps); cfg.Load.Sweep.Enabled && n > 0 && rr.SaturationTPS == cfg.Load.Sweep.Steps[n-1] {
		fmt.Fprintf(&b, "detected saturation: NOT REACHED - every step held; the knee is above %d TPS.\n"+
			"                     Extend the ladder to measure it.\n", rr.SaturationTPS)
	} else if rr.SaturationTPS > 0 {
		fmt.Fprintf(&b, "detected saturation: ~%d offered TPS\n", rr.SaturationTPS)
	} else if cfg.Load.Sweep.Enabled {
		fmt.Fprintf(&b, "detected saturation: NONE - no sweep step held (see the verdict column);\n"+
			"                     the headline below is a floor reading, not a saturation figure\n")
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
		"e2e_p50_ms", "e2e_p95_ms", "e2e_p99_ms", "e2e_p99.9_ms", "submit_p50_ms", "commit_p50_ms",
		// Appended, so existing positional readers keep working. These are what
		// tell a real saturation reading from a broken run.
		"goodput_ratio", "send_gap_p99_ms", "invariant_ok", "verdict"})
	for _, ph := range rr.Phases {
		r := ph.Result
		_ = w.Write([]string{
			ph.Name,
			strconv.Itoa(ph.OfferedTPS),
			f2(r.ConfirmedTPS), f2(r.OfferedTPS), f4(r.FailureRate),
			i64(r.Committed), i64(r.Submitted), i64(r.Invalid), i64(r.Errored), i64(r.TimedOut),
			f2(pctl(r.E2E, "p50")), f2(pctl(r.E2E, "p95")), f2(pctl(r.E2E, "p99")), f2(pctl(r.E2E, "p99.9")),
			f2(pctl(r.Submit, "p50")), f2(pctl(r.Commit, "p50")),
			f4(goodput(ph)), f2(pctl(r.SendGap, "p99")), strconv.FormatBool(r.InvariantOK), ph.Verdict,
		})
	}
	return nil
}

// goodput is confirmed throughput as a fraction of the phase's nominal offered
// rate. It is the column that exposes a collapse the failure rate hides.
func goodput(ph PhaseResult) float64 {
	if ph.OfferedTPS <= 0 {
		return 0
	}
	return ph.Result.ConfirmedTPS / float64(ph.OfferedTPS)
}

func f2(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
func f4(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }
func i64(i int64) string  { return strconv.FormatInt(i, 10) }

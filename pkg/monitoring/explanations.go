package monitoring

const (
	systemCPUChartTitle = "Harness CPU sampling (docker stats)"
	systemMemChartTitle = "Harness memory sampling (docker stats)"
)

// panelExplanations gives one plain-English sentence per dashboard panel
// title (dashboards/grafana/{overview,per-platform}.json), adapted from those
// dashboards' own note/caveat text panels so tone and meaning stay consistent
// with what's already reviewed there.
var panelExplanations = map[string]string{
	"Container CPU cores (cAdvisor)":                   "CPU cores used by this platform's own containers during the run. High values explain resource pressure; they are not a fairness metric on their own.",
	"Container memory (RSS)":                           "Resident memory used by this platform's own containers during the run.",
	"Host CPU utilisation (node_exporter)":             "Fraction of the test machine's CPU that was busy. If this saturates, the bottleneck may be the test machine, not the platform under test.",
	"Host disk IO (bytes/s)":                           "Disk read/write throughput on the test machine — host-level resource context, not a platform metric.",
	"Native /metrics up (informational only)":          "Whether the platform's own /metrics endpoint was reachable at each sample point; gaps mean scrape failures, not necessarily platform downtime.",
	"Fabric/Drunix: blocks committed rate":             "Blocks committed per second, from the platform's own ledger metric. Informational only — block size/batching config differs per platform, so this is not comparable across platforms.",
	"Fabric/Drunix: endorsement proposal duration p99": "p99 time for a peer to simulate and endorse one proposal, as reported by the platform itself. Excluded from cross-platform TPS comparisons, but useful for explaining where time goes inside one platform.",
	systemCPUChartTitle:                                "CPU% summed across this run's containers, sampled directly by the harness via `docker stats` once per second. Independent of Prometheus - the one chart that still renders even when the monitoring stack is down.",
	systemMemChartTitle:                                "Resident memory summed across this run's containers, sampled the same way as the CPU chart above - a coarse fallback, not a substitute for the cAdvisor chart when Prometheus is available.",
}

// nativeScrapeCaveat is reused verbatim (paraphrased for HTML) from
// docs/architecture/metrics-methodology.md's native-metrics section, so the
// report's wording doesn't drift from the documented fairness rule.
const nativeScrapeCaveat = "Raw values from the platform's own /metrics endpoint, captured once at the end of the run. Informational only - never used in cross-platform comparison (see docs/architecture/fairness-guarantees.md)."

// headlineExplanation and saturationExplanation caption the headline stat tiles.
const (
	headlineExplanation   = "The measurement window used as this run's single-number result - the hold phase for a sweep, or the only phase otherwise."
	saturationExplanation = "Highest offered load (TPS) in the probe-and-sweep whose failure rate stayed under the configured threshold - the detected throughput ceiling."
	percentileTableNote   = "Full latency spectrum from the HDR histogram (3 significant figures) backing this phase's measurement window. See docs/architecture/metrics-methodology.md."
)

// harnessMetricExplanations mirrors the doc comments already on
// pkg/metrics/collector.go, condensed to one sentence, so the report's wording
// matches the code's own definitions instead of drifting from them.
var harnessMetricExplanations = map[string]string{
	"ConfirmedTPS": "Committed transactions per second, using only T3 (observed-committed) timestamps.",
	"OfferedTPS":   "Submitted transactions per second — the load actually offered to the platform.",
	"FailureRate":  "(Invalid + Errored + TimedOut) / Submitted for this measurement window.",
	"E2E":          "End-to-end latency from the load generator's scheduled send time to observed finality (open-loop, so queueing delay can't hide behind a busy generator).",
	"Submit":       "Latency from the adapter's Submit call entering to the platform acknowledging receipt.",
	"Commit":       "Latency from platform acknowledgement to observed commit in a block.",
	"SendGap":      "How far behind schedule the load generator ran (actual send time minus scheduled send time). A large p99 here means the generator, not the platform, was the bottleneck.",
}

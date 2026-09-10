package metrics

import (
	"sort"
	"sync"
	"time"
)

// Outcome classifies the terminal state of one transaction.
type Outcome int

const (
	// OutcomePending means submitted but no finality observed yet.
	OutcomePending Outcome = iota
	// OutcomeCommitted means committed and valid - this is the only state that
	// counts as throughput.
	OutcomeCommitted
	// OutcomeInvalid means committed but marked invalid (MVCC conflict, policy
	// failure). Counts as a failure.
	OutcomeInvalid
	// OutcomeError means the submit call failed or finality timed out. Counts as
	// a failure.
	OutcomeError
	// OutcomeTimeout means WaitForFinality returned without a result before the
	// deadline. Counts as a failure.
	OutcomeTimeout
)

// TxRecord is the full timing history of one transaction. All times are absolute
// wall-clock; deltas are derived in Finalize.
type TxRecord struct {
	Seq uint64
	ID  string

	// ScheduledSend is when the load generator *intended* to send this
	// transaction. For open-loop runs latency is measured from here, not from
	// ActualSend, so a backlog cannot hide queueing delay (coordinated omission).
	// See docs/architecture/metrics-methodology.md.
	ScheduledSend time.Time

	T1 time.Time // adapter Submit entered
	T2 time.Time // platform acknowledged receipt
	T3 time.Time // observed committed in a block

	Outcome  Outcome
	BlockNum uint64
	Err      string
}

// e2e returns end-to-end latency from the scheduled send time to finality.
func (r *TxRecord) e2e() time.Duration     { return r.T3.Sub(r.ScheduledSend) }
func (r *TxRecord) submit() time.Duration  { return r.T2.Sub(r.T1) }
func (r *TxRecord) commit() time.Duration  { return r.T3.Sub(r.T2) }
func (r *TxRecord) sendGap() time.Duration { return r.T1.Sub(r.ScheduledSend) }

// Collector accumulates TxRecords during a run. It is written concurrently by
// many goroutines: one Add per submit, one Complete per finality.
type Collector struct {
	mu   sync.Mutex
	recs map[string]*TxRecord
	seq  map[uint64]string // seq -> id, for records whose id is not yet known
}

// NewCollector returns an empty Collector.
func NewCollector() *Collector {
	return &Collector{recs: map[string]*TxRecord{}, seq: map[uint64]string{}}
}

// Add registers a transaction at submit time.
func (c *Collector) Add(seq uint64, id string, scheduled, t1, t2 time.Time, submitErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := &TxRecord{Seq: seq, ID: id, ScheduledSend: scheduled, T1: t1, T2: t2}
	if submitErr != "" {
		r.Outcome = OutcomeError
		r.Err = submitErr
	}
	c.recs[id] = r
	c.seq[seq] = id
}

// Complete records the finality outcome for a previously added transaction.
func (c *Collector) Complete(id string, t3 time.Time, blockNum uint64, outcome Outcome, err string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.recs[id]
	if !ok {
		// Finality for a tx we never saw submitted - record it anyway so the
		// invariant check (submitted == committed+failed) can flag the gap.
		r = &TxRecord{ID: id}
		c.recs[id] = r
	}
	r.T3 = t3
	r.BlockNum = blockNum
	r.Outcome = outcome
	if err != "" {
		r.Err = err
	}
}

// Records returns a stable-ordered copy of all records collected so far.
func (c *Collector) Records() []*TxRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*TxRecord, 0, len(c.recs))
	for _, r := range c.recs {
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Window bounds the portion of a run that counts toward the result. Records
// whose ScheduledSend falls outside [Start, End) are excluded (warmup / cooldown).
type Window struct {
	Start time.Time
	End   time.Time
}

// Result is the aggregated outcome of one measurement window.
type Result struct {
	// WallClock is End-Start of the measurement window.
	WallClockSec float64 `json:"wall_clock_sec"`

	Submitted int64 `json:"submitted"`
	Committed int64 `json:"committed"`
	Invalid   int64 `json:"invalid"`
	Errored   int64 `json:"errored"`
	TimedOut  int64 `json:"timed_out"`

	// ConfirmedTPS = Committed / WallClock, using T3 timestamps only.
	ConfirmedTPS float64 `json:"confirmed_tps"`
	// OfferedTPS = Submitted / WallClock.
	OfferedTPS float64 `json:"offered_tps"`
	// FailureRate = (Invalid+Errored+TimedOut) / Submitted.
	FailureRate float64 `json:"failure_rate"`

	E2E    Snapshot `json:"e2e_latency"`
	Submit Snapshot `json:"submit_latency"`
	Commit Snapshot `json:"commit_latency"`
	// SendGap is T1-ScheduledSend: how far behind schedule the generator ran.
	// A large p99 send gap means the load generator, not the platform, was the
	// bottleneck - the run should be rejected.
	SendGap Snapshot `json:"send_gap"`

	// InvariantOK is false if submitted != committed+invalid+errored+timedout.
	InvariantOK bool `json:"invariant_ok"`
}

// Aggregate reduces the collected records over the given window.
func (c *Collector) Aggregate(w Window) Result {
	recs := c.Records()

	e2e := NewLatency("e2e")
	sub := NewLatency("submit")
	com := NewLatency("commit")
	gap := NewLatency("send_gap")

	var res Result
	res.WallClockSec = w.End.Sub(w.Start).Seconds()

	var terminal int64
	for _, r := range recs {
		// Window on scheduled send time so warmup/cooldown cut the same
		// absolute slice for every platform.
		if !r.ScheduledSend.IsZero() && (r.ScheduledSend.Before(w.Start) || !r.ScheduledSend.Before(w.End)) {
			continue
		}
		res.Submitted++

		switch r.Outcome {
		case OutcomeCommitted:
			res.Committed++
			terminal++
			if !r.T3.IsZero() && !r.T1.IsZero() {
				e2e.Record(r.e2e())
				sub.Record(r.submit())
				com.Record(r.commit())
				gap.Record(r.sendGap())
			}
		case OutcomeInvalid:
			res.Invalid++
			terminal++
		case OutcomeError:
			res.Errored++
			terminal++
		case OutcomeTimeout:
			res.TimedOut++
			terminal++
		case OutcomePending:
			// still in flight at window close - not terminal, not counted
		}
	}

	if res.WallClockSec > 0 {
		res.ConfirmedTPS = float64(res.Committed) / res.WallClockSec
		res.OfferedTPS = float64(res.Submitted) / res.WallClockSec
	}
	if res.Submitted > 0 {
		res.FailureRate = float64(res.Invalid+res.Errored+res.TimedOut) / float64(res.Submitted)
	}
	res.E2E = e2e.Snapshot()
	res.Submit = sub.Snapshot()
	res.Commit = com.Snapshot()
	res.SendGap = gap.Snapshot()
	res.InvariantOK = terminal == res.Submitted

	return res
}

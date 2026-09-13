package metrics

import (
	"testing"
	"time"
)

func TestAggregateWindowAndInvariant(t *testing.T) {
	c := NewCollector()
	base := time.Now()

	// 10 tx scheduled at 100ms spacing: [0,900]ms. Window [250,750) keeps
	// scheduled times 300,400,500,600,700 => 5 in-window.
	for i := 0; i < 10; i++ {
		sched := base.Add(time.Duration(i) * 100 * time.Millisecond)
		id := "tx-" + time.Duration(i).String()
		c.Add(uint64(i), id, sched, sched, sched.Add(2*time.Millisecond), "")
		c.Complete(id, sched.Add(40*time.Millisecond), uint64(i), OutcomeCommitted, "")
	}

	res := c.Aggregate(Window{Start: base.Add(250 * time.Millisecond), End: base.Add(750 * time.Millisecond)})
	if res.Submitted != 5 {
		t.Fatalf("expected 5 in-window submitted, got %d", res.Submitted)
	}
	if res.Committed != 5 {
		t.Fatalf("expected 5 committed, got %d", res.Committed)
	}
	if !res.InvariantOK {
		t.Fatal("invariant should hold")
	}
	// e2e = T3 - ScheduledSend = 40ms for every kept record.
	if p50 := res.E2E.Percentiles["p50"]; p50 < 38 || p50 > 42 {
		t.Fatalf("e2e p50 = %.2f ms, want ~40", p50)
	}
}

func TestAggregateCountsFailures(t *testing.T) {
	c := NewCollector()
	base := time.Now()
	mk := func(i int, oc Outcome) {
		id := "f-" + time.Duration(i).String()
		s := base.Add(time.Duration(i) * time.Millisecond)
		c.Add(uint64(i), id, s, s, s, "")
		c.Complete(id, s.Add(time.Millisecond), 0, oc, "")
	}
	mk(0, OutcomeCommitted)
	mk(1, OutcomeInvalid)
	mk(2, OutcomeError)
	mk(3, OutcomeTimeout)

	res := c.Aggregate(Window{Start: base.Add(-time.Second), End: base.Add(time.Second)})
	if res.Committed != 1 || res.Invalid != 1 || res.Errored != 1 || res.TimedOut != 1 {
		t.Fatalf("bad tallies: %+v", res)
	}
	if res.FailureRate < 0.74 || res.FailureRate > 0.76 {
		t.Fatalf("failure rate %.3f, want 0.75", res.FailureRate)
	}
	if !res.InvariantOK {
		t.Fatal("invariant should hold with all-terminal records")
	}
}

func TestInvariantDetectsPending(t *testing.T) {
	c := NewCollector()
	base := time.Now()
	c.Add(0, "p0", base, base, base, "")
	// never Complete'd -> still pending
	res := c.Aggregate(Window{Start: base.Add(-time.Second), End: base.Add(time.Second)})
	if res.InvariantOK {
		t.Fatal("invariant must fail when a submitted tx never reached a terminal state")
	}
}

// A failed phase must say why. Per-transaction ids in the message must not split
// one cause into one entry per transaction.
func TestAggregateGroupsErrorMessages(t *testing.T) {
	c := NewCollector()
	base := time.Now()
	for i := 0; i < 7; i++ {
		id := "t" + string(rune('a'+i))
		c.Add(uint64(i), id, base, base, base, "")
		c.Complete(id, base, 0, OutcomeTimeout, "cp finality timeout for 3cb9c5dde53a0fede3bc87925928042b"+string(rune('0'+i)))
	}
	c.Add(7, "e", base, base, base, "rpc error: code = Unavailable")
	c.Add(8, "ok", base, base, base, "")
	c.Complete("ok", base, 1, OutcomeCommitted, "")

	res := c.Aggregate(Window{Start: base.Add(-time.Second), End: base.Add(time.Second)})
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2 distinct causes", res.Errors)
	}
	if res.Errors[0].Count != 7 || res.Errors[0].Message != "cp finality timeout for N" {
		t.Errorf("top error = %+v, want 7 x the normalized finality timeout", res.Errors[0])
	}
	if res.Errors[1].Count != 1 {
		t.Errorf("second error = %+v, want count 1", res.Errors[1])
	}
}

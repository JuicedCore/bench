package neuchain

import (
	"context"
	"sync"
	"testing"
	"time"
)

// T3 must be the instant the poller observed the block, not the instant
// WaitForFinality happened to return. Stamping on return folds in the poll
// interval and, when the tx resolved before the caller asked, however long the
// caller took to ask - inflating NeuChain's end-to-end latency against Fabric,
// Drunix and Fabric-X, which all stamp at observation.
// See docs/architecture/fairness-guarantees.md.

func newTestAdapter() *Adapter {
	return &Adapter{
		mu:       sync.Mutex{},
		waiters:  map[string]chan observedFrame{},
		resolved: map[string]observedFrame{},
	}
}

// The pre-resolved path is the arbitrarily-late case: the poller saw the block
// well before the caller got round to waiting on it.
func TestFinalityTimeIsObservationNotWaitReturn(t *testing.T) {
	a := newTestAdapter()

	observed := time.Now()
	a.resolved["deadbeef"] = observedFrame{
		frame: resultFrame{TID: 1, Epoch: 7, Result: resCommit},
		at:    observed,
	}

	// Caller shows up late.
	time.Sleep(40 * time.Millisecond)

	res, err := a.WaitForFinality(context.Background(), "deadbeef", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FinalityTime.Equal(observed) {
		t.Errorf("FinalityTime = %v, want the observation instant %v (drift %v)",
			res.FinalityTime, observed, res.FinalityTime.Sub(observed))
	}
	if !res.Valid {
		t.Error("expected a COMMIT frame to be valid")
	}
	if res.BlockNum != 7 {
		t.Errorf("BlockNum = %d, want the epoch 7", res.BlockNum)
	}
}

// The waiter path: the caller is already blocked when the poller delivers.
func TestFinalityTimeFromWaiterUsesObservationInstant(t *testing.T) {
	a := newTestAdapter()

	ch := make(chan observedFrame, 1)
	a.waiters["cafe"] = ch

	observed := time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		ch <- observedFrame{
			frame: resultFrame{TID: 2, Epoch: 9, Result: resCommit},
			at:    observed,
		}
	}()

	res, err := a.WaitForFinality(context.Background(), "cafe", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FinalityTime.Equal(observed) {
		t.Errorf("FinalityTime = %v, want the observation instant %v (drift %v)",
			res.FinalityTime, observed, res.FinalityTime.Sub(observed))
	}
}

// An aborted frame is a platform-side invalid, not an adapter error: it must come
// back as a terminal result with Valid=false so the failure-rate breakdown can
// tell it apart from an unreachable platform.
func TestAbortedFrameIsInvalidNotError(t *testing.T) {
	a := newTestAdapter()
	a.resolved["bad"] = observedFrame{
		frame: resultFrame{TID: 3, Epoch: 1, Result: resAbort},
		at:    time.Now(),
	}
	res, err := a.WaitForFinality(context.Background(), "bad", time.Second)
	if err != nil {
		t.Fatalf("aborted tx should be a terminal result, not an error: %v", err)
	}
	if res.Valid {
		t.Error("ABORT frame reported as valid")
	}
}

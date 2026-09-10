package loadgen

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/metrics"
)

// TxSource produces the next transaction for a sequence number. workloads.Workload
// satisfies it.
type TxSource interface {
	Next(seq uint64) *adapters.Transaction
}

// Mode is the load-generation strategy.
type Mode string

const (
	// OpenLoop fires transactions at a target offered rate regardless of
	// backpressure. Latency is measured from the *scheduled* send time so a
	// backlog cannot mask overload (coordinated-omission safe).
	OpenLoop Mode = "open-loop"
	// ClosedLoop keeps a fixed number of workers, each submitting then waiting
	// for finality before the next submit. Measures capacity at a concurrency.
	ClosedLoop Mode = "closed-loop"
)

// LoadProfile is the full description of one load phase.
type LoadProfile struct {
	Mode Mode

	// Open-loop:
	StartTPS int           // offered rate at t=0 (after ramp this is ignored if RampFrom set)
	TargetTPS int          // offered rate to hold
	RampFrom  int          // if >0, linearly ramp offered rate RampFrom->TargetTPS over RampDur
	RampDur   time.Duration

	// Closed-loop:
	Workers int

	// Both:
	Duration       time.Duration // total phase length including ramp
	FinalityWait   time.Duration // per-tx WaitForFinality timeout
	FinalityPool   int           // open-loop: concurrent finality waiters (default 4*sqrt(TargetTPS))
	MaxInFlight    int           // open-loop: cap on outstanding submits; 0 = unbounded
}

// Generator drives one adapter with one workload for the duration of a phase.
type Generator struct {
	Adapter  adapters.PlatformAdapter
	Source   TxSource
	Collector *metrics.Collector

	seq uint64
}

// Run executes the phase and returns when Duration elapses and all in-flight
// finality waits have drained (or the context is cancelled).
func (g *Generator) Run(ctx context.Context, p LoadProfile) error {
	switch p.Mode {
	case ClosedLoop:
		return g.runClosed(ctx, p)
	case OpenLoop, "":
		return g.runOpen(ctx, p)
	default:
		return fmt.Errorf("unknown load mode %q", p.Mode)
	}
}

func (g *Generator) nextSeq() uint64 {
	// single generator goroutine schedules; workers only wait. Safe without atomic
	// in open-loop; closed-loop guards with its own increment.
	g.seq++
	return g.seq
}

// ---- open loop ----

func (g *Generator) runOpen(ctx context.Context, p LoadProfile) error {
	if p.FinalityWait <= 0 {
		p.FinalityWait = 30 * time.Second
	}
	finPool := p.FinalityPool
	if finPool <= 0 {
		finPool = 64
	}

	type pending struct {
		id        string
		scheduled time.Time
	}
	finCh := make(chan pending, finPool*4)
	var finWG sync.WaitGroup
	for i := 0; i < finPool; i++ {
		finWG.Add(1)
		go func() {
			defer finWG.Done()
			for pv := range finCh {
				g.awaitFinality(ctx, pv.id, p.FinalityWait)
			}
		}()
	}

	var subWG sync.WaitGroup
	inflight := make(chan struct{}, maxInflight(p))

	start := time.Now()
	deadline := start.Add(p.Duration)
	// Schedule loop: compute the ideal send time of tx k and sleep until it.
	var k uint64
	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		rate := offeredRate(p, now.Sub(start))
		if rate <= 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		scheduled := start.Add(time.Duration(float64(k) / float64(rate) * float64(time.Second)))
		if scheduled.After(deadline) {
			break
		}
		if d := time.Until(scheduled); d > 0 {
			select {
			case <-ctx.Done():
				goto drain
			case <-time.After(d):
			}
		}
		k++

		select {
		case inflight <- struct{}{}:
		case <-ctx.Done():
			goto drain
		}

		seq := g.nextSeq()
		tx := g.Source.Next(seq)
		subWG.Add(1)
		go func(tx *adapters.Transaction, scheduled time.Time) {
			defer subWG.Done()
			defer func() { <-inflight }()
			id, ok := g.submit(ctx, tx, scheduled)
			if ok {
				select {
				case finCh <- pending{id: id, scheduled: scheduled}:
				case <-ctx.Done():
				}
			}
		}(tx, scheduled)
	}

drain:
	subWG.Wait()
	close(finCh)
	finWG.Wait()
	return ctx.Err()
}

func maxInflight(p LoadProfile) int {
	if p.MaxInFlight > 0 {
		return p.MaxInFlight
	}
	// generous default: 4s worth of offered load
	m := p.TargetTPS * 4
	if m < 256 {
		m = 256
	}
	return m
}

// offeredRate returns the target offered TPS at elapsed time e.
func offeredRate(p LoadProfile, e time.Duration) float64 {
	if p.RampFrom > 0 && p.RampDur > 0 {
		if e >= p.RampDur {
			return float64(p.TargetTPS)
		}
		frac := float64(e) / float64(p.RampDur)
		return float64(p.RampFrom) + frac*float64(p.TargetTPS-p.RampFrom)
	}
	if p.StartTPS > 0 && p.StartTPS != p.TargetTPS && p.RampDur > 0 {
		if e >= p.RampDur {
			return float64(p.TargetTPS)
		}
		frac := float64(e) / float64(p.RampDur)
		return float64(p.StartTPS) + frac*float64(p.TargetTPS-p.StartTPS)
	}
	return float64(p.TargetTPS)
}

// ---- closed loop ----

func (g *Generator) runClosed(ctx context.Context, p LoadProfile) error {
	if p.Workers <= 0 {
		p.Workers = 1
	}
	if p.FinalityWait <= 0 {
		p.FinalityWait = 30 * time.Second
	}
	deadline := time.Now().Add(p.Duration)

	var mu sync.Mutex
	next := func() uint64 { mu.Lock(); defer mu.Unlock(); g.seq++; return g.seq }

	var wg sync.WaitGroup
	for i := 0; i < p.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if ctx.Err() != nil {
					return
				}
				seq := next()
				tx := g.Source.Next(seq)
				// In closed loop the scheduled time IS the submit time: there is
				// no offered-rate schedule to fall behind.
				scheduled := time.Now()
				id, ok := g.submit(ctx, tx, scheduled)
				if !ok {
					continue
				}
				g.awaitFinality(ctx, id, p.FinalityWait)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// ---- shared submit / finality ----

// submit calls the adapter and records T1/T2. Returns the tx id and whether it
// was accepted (finality should be awaited).
func (g *Generator) submit(ctx context.Context, tx *adapters.Transaction, scheduled time.Time) (string, bool) {
	t1 := time.Now()
	res, err := g.Adapter.Submit(ctx, tx)
	if err != nil {
		id := fmt.Sprintf("seq-%d-failed", tx.Seq)
		if res != nil && res.TxID != "" {
			id = res.TxID
		}
		g.Collector.Add(tx.Seq, id, scheduled, t1, time.Now(), err.Error())
		return id, false
	}
	t2 := res.AckTime
	if t2.IsZero() {
		t2 = time.Now()
	}
	// T1 is captured by the generator immediately before Submit so it is measured
	// identically for every platform; res.SubmitTime is advisory only.
	g.Collector.Add(tx.Seq, res.TxID, scheduled, t1, t2, "")
	return res.TxID, true
}

func (g *Generator) awaitFinality(ctx context.Context, id string, wait time.Duration) {
	fr, err := g.Adapter.WaitForFinality(ctx, id, wait)
	now := time.Now()
	switch {
	case err != nil:
		g.Collector.Complete(id, now, 0, metrics.OutcomeTimeout, err.Error())
	case fr == nil:
		g.Collector.Complete(id, now, 0, metrics.OutcomeTimeout, "nil finality result")
	case !fr.Valid:
		g.Collector.Complete(id, tsOr(fr.FinalityTime, now), fr.BlockNum, metrics.OutcomeInvalid, "")
	default:
		g.Collector.Complete(id, tsOr(fr.FinalityTime, now), fr.BlockNum, metrics.OutcomeCommitted, "")
	}
}

func tsOr(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

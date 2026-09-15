// Package neuchain is the PlatformAdapter for NeuChain (ordering-free
// Execute-Validate, C++). PURE-GO client: ZeroMQ + protobuf + RSA-1024/SHA-256,
// no cgo, no native `user` binary. See the spike write-up in
// docs/platforms/neuchain-client-implementation.md and adr-002.
//
//	submit    -> ZMQ PUB  to <block-server>:5001, comm.UserRequest{payload, digest=sig}
//	finality  -> ZMQ REQ  to <block-server>:7003, tip_query / block_query polling;
//	             the poller decodes each block's hand-rolled result frames and
//	             resolves waiting transactions by digest.
package neuchain

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "neuchain"

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter implements adapters.PlatformAdapter for NeuChain.
type Adapter struct {
	cfg    *Config
	signer *signer
	tr     *zmqTransport

	pollCtx    context.Context
	pollCancel context.CancelFunc
	pollDone   chan struct{}
	hbDone     chan struct{}

	log *slog.Logger

	mu sync.Mutex
	// waiters holds every submitted tx until WaitForFinality returns. Submit
	// registers it before publishing and the channel is buffered, so a frame the
	// poller sees before the caller waits is kept there, stamped at observation.
	waiters map[string]chan observedFrame // hex(digest) -> signal
	// pollErr is set while the poller has been unable to query the chain for
	// longer than pollDownAfter; WaitForFinality then fails fast with it.
	pollErr       error
	skippedBlocks int
}

// heartbeat keeps NeuChain's epochs moving, as NeuChain's own deployment does
// with `user` in low-cost mode: an "empty" transaction to every block server each
// HeartbeatInterval. A block for epoch N is only emitted once later epochs carry
// traffic, so without it the last transactions of a burst (or of the run) stay
// pending indefinitely. The chaincode aborts "empty" without touching state.
func (a *Adapter) heartbeat() {
	defer close(a.hbDone)
	t := time.NewTicker(a.cfg.HeartbeatInterval)
	defer t.Stop()
	tr := a.tr
	var seq uint64
	fails := 0
	for {
		select {
		case <-a.pollCtx.Done():
			return
		case <-t.C:
		}
		for i := range tr.pub {
			seq++
			wire, err := a.buildHeartbeat(seq)
			if err == nil {
				err = tr.publishTo(i, wire)
			}
			if err != nil && a.pollCtx.Err() == nil {
				if fails++; fails == 1 || fails%100 == 0 {
					a.logger().Warn("heartbeat publish failed; tail transactions may not finalize while this lasts", "err", err, "consecutive", fails)
				}
				continue
			}
			fails = 0
		}
	}
}

// Poller failure thresholds.
const (
	// pollDownAfter is how long tip queries may fail continuously before
	// finality is reported as down rather than slow.
	pollDownAfter = 30 * time.Second
	// maxBlockRetries is how many ticks a block that cannot be fetched or decoded
	// is retried before the poller skips it. Without a limit one bad block stalls
	// finality for every later transaction.
	maxBlockRetries = 40
)

func (a *Adapter) logger() *slog.Logger {
	if a.log != nil {
		return a.log
	}
	return slog.Default()
}

// observedFrame pairs a decoded result frame with the instant the poller saw the
// block carrying it. T3 must be that instant: stamping it when WaitForFinality
// returns instead would fold in the poll interval and, for a tx already resolved
// before the caller asked, however long the caller took to ask. Fabric, Drunix
// and Fabric-X all stamp at observation; this keeps NeuChain on the same footing
// (docs/architecture/fairness-guarantees.md).
type observedFrame struct {
	frame resultFrame
	at    time.Time
}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) PlatformVersion() string { return platformName }

// MetricsEndpoint is whatever the run config set; empty when the NeuChain build
// was not compiled with metrics, which disables the end-of-run native scrape.
func (a *Adapter) MetricsEndpoint() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.MetricsEndpointURL
}

// CryptoInfo: NeuChain verifies the user's RSA signature once on submit; there is
// no endorsement round. per_tx_endorsement_verify=false is the right flag for
// the cross-platform table (see neuchain-client-implementation.md §5).
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "RSA-1024-PKCS1v15",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: false,
		MSPNote:                "NeuChain EV: RSA-1024 user signature over the serialized TransactionPayload; no endorsement phase, ordering implicit via deterministic execution",
	}
}

func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	a.log = ac.Log()
	cfg, err := configFromExtra(ac.Extra)
	if err != nil {
		return err
	}
	a.cfg = cfg
	if a.signer, err = loadSigner(cfg.UserPrivKeyPath, cfg.KeyPassword); err != nil {
		return err
	}
	// The transport lives for the whole run, so it gets its own context rather
	// than Setup's; the dial itself is bounded by adapter.dial_timeout.
	if a.tr, err = newTransport(context.Background(), cfg, a.signer.sign); err != nil {
		return err
	}

	a.waiters = map[string]chan observedFrame{}

	// Verify the query path is alive (tip may legitimately be 0 pre-genesis).
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tip, err := a.pingTip(tctx)
	if err != nil {
		a.tr.Close()
		a.tr = nil
		return fmt.Errorf("neuchain: query endpoint %s did not answer tip_query within 5s: %w (is the block server up and its query port published? docker ps | grep block-server)", cfg.QueryEndpoint, err)
	}
	if cfg.StartBlock == 0 {
		cfg.StartBlock = tip + 1
	} else if cfg.StartBlock+1000 < tip {
		a.log.Warn("start_block is far behind the chain tip: the poller will replay history before it sees this run's transactions, delaying their observed finality",
			"start_block", cfg.StartBlock, "tip", tip)
	}

	a.pollCtx, a.pollCancel = context.WithCancel(context.Background())
	a.pollDone = make(chan struct{})
	go a.poll()
	if cfg.HeartbeatInterval > 0 {
		a.hbDone = make(chan struct{})
		go a.heartbeat()
	}
	a.log.Info("setup complete", "block_servers", cfg.BlockServers, "query", cfg.QueryEndpoint, "tip", tip, "start_block", cfg.StartBlock, "poll_interval", cfg.PollInterval)
	return nil
}

func (a *Adapter) pingTip(ctx context.Context) (uint64, error) {
	type res struct {
		tip uint64
		err error
	}
	done := make(chan res, 1)
	go func() { n, err := a.tr.tip(); done <- res{n, err} }()
	select {
	case r := <-done:
		return r.tip, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Teardown stops the poller and closes the sockets. The transport is closed
// first: that cancels any query the poller is blocked in, so Teardown cannot
// hang for a socket timeout waiting on it. Safe to call more than once.
func (a *Adapter) Teardown(ctx context.Context) error {
	if a.pollCancel != nil {
		a.pollCancel()
	}
	if a.tr != nil {
		a.tr.Close()
		a.tr = nil
	}
	for name, done := range map[string]*chan struct{}{"finality poller": &a.pollDone, "heartbeat": &a.hbDone} {
		if *done == nil {
			continue
		}
		select {
		case <-*done:
		case <-ctx.Done():
			return fmt.Errorf("neuchain: %s did not stop before teardown deadline: %w", name, ctx.Err())
		}
		*done = nil
	}
	a.pollCancel = nil
	return nil
}

// Submit builds, signs and publishes one transaction. T2 = the moment the ZMQ
// send returns (NeuChain PUB is fire-and-forget - there is no ack).
func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	if a.tr == nil {
		return nil, fmt.Errorf("neuchain: %w", adapters.ErrNotSetUp)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t1 := time.Now()
	wire, sig, err := a.buildInvoke(tx)
	if err != nil {
		return &adapters.SubmitResult{SubmitTime: t1}, fmt.Errorf("neuchain: build transaction: %w", err)
	}
	id := hex.EncodeToString(sig)

	// Register the waiter before publishing so the poller can never miss it.
	a.mu.Lock()
	if _, dup := a.waiters[id]; !dup {
		a.waiters[id] = make(chan observedFrame, 1)
	}
	a.mu.Unlock()

	if err := a.tr.publish(wire); err != nil {
		a.mu.Lock()
		delete(a.waiters, id)
		a.mu.Unlock()
		return &adapters.SubmitResult{TxID: id, SubmitTime: t1}, err
	}
	return &adapters.SubmitResult{TxID: id, SubmitTime: t1, AckTime: time.Now()}, nil
}

// WaitForFinality blocks until the poller sees txID in a committed block.
func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	a.mu.Lock()
	ch, ok := a.waiters[txID]
	if !ok {
		ch = make(chan observedFrame, 1)
		a.waiters[txID] = ch
	}
	pollErr := a.pollErr
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.waiters, txID)
		a.mu.Unlock()
	}()
	if pollErr != nil {
		return nil, pollErr
	}

	select {
	case f := <-ch:
		return finality(txID, f), nil
	case <-time.After(timeout):
		a.mu.Lock()
		pollErr, skipped := a.pollErr, a.skippedBlocks
		a.mu.Unlock()
		if pollErr != nil {
			return nil, pollErr
		}
		note := ""
		if skipped > 0 {
			note = fmt.Sprintf(" (%d undecodable block(s) were skipped this run; see run.log)", skipped)
		}
		return nil, fmt.Errorf("neuchain: %w: tx %.16s... not seen in any polled block within %s%s",
			adapters.ErrFinalityTimeout, txID, timeout, note)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Query is a state read via NeuChain's query path. Not wired to a dedicated RPC
// here (the block-query path returns results, not point reads); returns
// not-found so workload verification degrades gracefully rather than lying.
func (a *Adapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return &adapters.QueryResult{Found: false}, nil
}

// poll tracks the chain tip and decodes each new block's result frames,
// resolving waiting transactions by digest.
//
// Failures are logged (first occurrence and then every 100th), and bounded: a
// query path that stays down for pollDownAfter makes WaitForFinality fail fast
// with adapters.ErrFinalityStreamDown, and a block that cannot be fetched or
// decoded for maxBlockRetries ticks is skipped so it cannot stall every later
// transaction.
func (a *Adapter) poll() {
	defer close(a.pollDone)
	log := a.logger().With("component", "finality-poller", "query", a.cfg.QueryEndpoint)
	next := a.cfg.StartBlock
	t := time.NewTicker(a.cfg.PollInterval)
	defer t.Stop()
	tr := a.tr

	var tipFailSince time.Time
	tipFails, blockFails := 0, 0
	for {
		select {
		case <-a.pollCtx.Done():
			return
		case <-t.C:
		}

		tip, err := tr.tip()
		if err != nil {
			if a.pollCtx.Err() != nil {
				return
			}
			tipFails++
			if tipFailSince.IsZero() {
				tipFailSince = time.Now()
			}
			if tipFails == 1 || tipFails%100 == 0 {
				log.Warn("tip query failed; finality is not being observed while this lasts", "err", err, "consecutive", tipFails)
			}
			if since := time.Since(tipFailSince); since > pollDownAfter {
				a.mu.Lock()
				if a.pollErr == nil {
					a.pollErr = fmt.Errorf("neuchain: %w: tip_query to %s has failed for %s: %v",
						adapters.ErrFinalityStreamDown, a.cfg.QueryEndpoint, since.Round(time.Second), err)
					log.Error("finality poller is down", "err", a.pollErr)
				}
				a.mu.Unlock()
			}
			continue
		}
		if tipFails > 0 {
			log.Info("tip query recovered", "after_failures", tipFails)
			a.mu.Lock()
			a.pollErr = nil
			a.mu.Unlock()
		}
		tipFails, tipFailSince = 0, time.Time{}
		if tip < next {
			continue
		}
		for n := next; n <= tip; n++ {
			frames, err := tr.block(n)
			if err != nil {
				blockFails++
				if blockFails == 1 {
					log.Warn("block query failed; retrying this height", "block", n, "err", err)
				}
				if blockFails < maxBlockRetries {
					break // retry this height next tick
				}
				if !errors.Is(err, errEmptyBlock) {
					a.mu.Lock()
					a.skippedBlocks++
					a.mu.Unlock()
					log.Error("skipping block after repeated failures; transactions in it will time out", "block", n, "attempts", blockFails, "err", err)
				} else {
					log.Debug("skipping block with no data", "block", n)
				}
				// frames holds whatever decoded before the failure; deliver those.
			}
			blockFails = 0
			// One stamp per block fetch: this is T3 for every tx it carries.
			observedAt := time.Now()
			for _, f := range frames {
				id := hex.EncodeToString(f.Digest)
				o := observedFrame{frame: f, at: observedAt}
				// Frames with no waiter are heartbeats, other clients' txs, or txs
				// whose wait already timed out; keeping them would grow without bound.
				a.mu.Lock()
				if ch, ok := a.waiters[id]; ok {
					select {
					case ch <- o:
					default:
					}
				}
				a.mu.Unlock()
			}
			next = n + 1
		}
	}
}

// finality reports T3 as the moment the poller OBSERVED the block, carried on
// the observedFrame, not the moment this function ran. BlockNum carries NeuChain's
// epoch: the EV path has no ledger height, and the manifest caveat says so.
func finality(txID string, o observedFrame) *adapters.FinalityResult {
	return &adapters.FinalityResult{
		TxID:         txID,
		FinalityTime: o.at,
		BlockNum:     o.frame.Epoch,
		Valid:        o.frame.valid(),
	}
}

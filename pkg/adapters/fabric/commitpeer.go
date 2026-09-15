package fabric

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc"

	"github.com/juicedcore/bench/pkg/adapters"
)

// cpListener watches a peer's filtered-block event stream and resolves transaction
// finality from there, stamping T3 the moment the block is decoded.
//
// Every Fabric-family platform uses it. For Drunix it is required: the Gateway
// runs on the Lite Peer, which never commits, so `Commit.Status()` never fires and
// the Committing Peer's stream is the only source. For fabric-cft/bft it is about
// fairness: `Commit.Status()` is called per transaction by a bounded pool of
// finality workers, so T3 used to be stamped when a worker got round to asking -
// queueing delay billed as platform latency - while Fabric-X and NeuChain stamp
// at observation. Reading the gateway peer's own block stream puts all of them on
// the same rule (docs/architecture/fairness-guarantees.md).
//
// The stream can end mid-run (peer restart, network blip). The gateway SDK closes
// the event channel without saying why, so the listener re-subscribes from the
// block after the last one it saw, with backoff. If it cannot get a block flowing
// again it marks itself down, and every waiter fails with
// adapters.ErrFinalityStreamDown instead of timing out one by one.
type cpListener struct {
	conn   *grpc.ClientConn
	gw     *client.Gateway
	cancel context.CancelFunc
	done   chan struct{}

	endpoint, channel string
	log               *slog.Logger

	mu        sync.Mutex
	resolved  map[string]cpResult      // txid -> outcome (for finality seen before the wait)
	waiters   map[string]chan cpResult // txid -> signal
	lastErr   error                    // non-nil once the stream is down for good
	down      chan struct{}            // closed when lastErr is set
	lastPrune time.Time
}

type cpResult struct {
	blockNum uint64
	valid    bool
	code     string // validation code name when !valid
	at       time.Time
}

// maxResubscribes is how many consecutive re-subscriptions may end without
// delivering a block before the listener gives up.
const maxResubscribes = 6

// startCPListener dials the Committing Peer, opens a filtered-block stream on the
// channel, and indexes every transaction's validation code.
func startCPListener(endpoint, tlsCACertPath, serverNameOverride, channel string,
	id *identity.X509Identity, sign identity.Sign, log *slog.Logger) (*cpListener, error) {

	conn, err := dial(endpoint, tlsCACertPath, serverNameOverride)
	if err != nil {
		return nil, fmt.Errorf("committing peer %s: %w", endpoint, err)
	}
	gw, err := client.Connect(id, client.WithSign(sign), client.WithClientConnection(conn))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("committing peer %s: gateway connect: %w", endpoint, err)
	}

	l, err := listenBlockEvents(gw, endpoint, channel, log)
	if err != nil {
		gw.Close()
		conn.Close()
		return nil, fmt.Errorf("committing peer %s: %w", endpoint, err)
	}
	// This listener dialled its own connection, so it owns closing it.
	l.conn, l.gw = conn, gw
	return l, nil
}

// listenBlockEvents subscribes to filtered block events on an existing gateway.
// The returned listener does not own gw: closing it stops the stream only.
func listenBlockEvents(gw *client.Gateway, endpoint, channel string, log *slog.Logger) (*cpListener, error) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &cpListener{
		cancel: cancel, done: make(chan struct{}), down: make(chan struct{}),
		endpoint: endpoint, channel: channel,
		log:      log.With("stream", "filtered-block-events", "endpoint", endpoint, "channel", channel),
		resolved: map[string]cpResult{}, waiters: map[string]chan cpResult{},
	}
	network := gw.GetNetwork(channel)

	events, err := network.FilteredBlockEvents(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("filtered block events on channel %q: %w", channel, err)
	}
	l.log.Debug("block event stream open")

	go func() {
		defer close(l.done)
		var next uint64 // next block number expected; 0 = none seen yet
		sawBlock := false
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case blk, ok := <-events:
				if ok {
					failures = 0
					sawBlock = true
					num := blk.GetNumber()
					next = num + 1
					for _, ft := range blk.GetFilteredTransactions() {
						code := ft.GetTxValidationCode()
						r := cpResult{blockNum: num, valid: code == peer.TxValidationCode_VALID, at: time.Now()}
						if !r.valid {
							r.code = code.String()
						}
						l.deliver(ft.GetTxid(), r)
					}
					continue
				}
				if ctx.Err() != nil {
					return
				}
				failures++
				if failures > maxResubscribes {
					l.fail(fmt.Errorf("%w: block event stream from %s on channel %q closed %d times in a row without delivering a block "+
						"(the gateway SDK does not report why: check the peer is running - docker ps / docker logs - and that the channel name is right)",
						adapters.ErrFinalityStreamDown, endpoint, channel, failures))
					return
				}
				backoff := time.Duration(1<<(failures-1)) * 250 * time.Millisecond
				l.log.Warn("block event stream closed; re-subscribing (transactions committed meanwhile are replayed)",
					"attempt", failures, "backoff", backoff, "resume_block", next, "had_blocks", sawBlock)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				opts := []client.BlockEventsOption{}
				if sawBlock {
					opts = append(opts, client.WithStartBlock(next))
				}
				ev, serr := network.FilteredBlockEvents(ctx, opts...)
				if serr != nil {
					l.log.Warn("re-subscribe failed", "attempt", failures, "err", serr)
					// Keep a closed channel so the next loop iteration counts another failure.
					closed := make(chan *peer.FilteredBlock)
					close(closed)
					events = closed
					continue
				}
				events = ev
			}
		}
	}()
	return l, nil
}

// fail marks the stream down and wakes every waiter.
func (l *cpListener) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastErr != nil {
		return
	}
	l.lastErr = err
	close(l.down)
	l.log.Error("finality stream is down; every pending and future transaction on this run will fail", "err", err)
}

func (l *cpListener) deliver(txid string, r cpResult) {
	if txid == "" {
		return
	}
	l.mu.Lock()
	if ch, ok := l.waiters[txid]; ok {
		select {
		case ch <- r:
		default:
		}
	} else {
		l.resolved[txid] = r
		if len(l.resolved) > maxResolved && r.at.Sub(l.lastPrune) > 10*time.Second {
			l.lastPrune = r.at
			l.pruneLocked(r.at)
		}
	}
	l.mu.Unlock()
}

// maxResolved bounds finality results nobody has asked for yet: other clients'
// transactions, and ours whose submit reported an error but committed anyway.
// Past it, entries older than resolvedTTL are dropped so a long run does not
// grow the map without limit.
const (
	maxResolved = 200_000
	resolvedTTL = 5 * time.Minute
)

func (l *cpListener) pruneLocked(now time.Time) {
	n := 0
	for id, r := range l.resolved {
		if now.Sub(r.at) > resolvedTTL {
			delete(l.resolved, id)
			n++
		}
	}
	l.log.Debug("pruned unclaimed finality results", "dropped", n, "kept", len(l.resolved))
}

// wait blocks until the CP block stream reports txid, the timeout elapses, or
// the stream goes down for good.
func (l *cpListener) wait(ctx context.Context, txid string, timeout time.Duration) (cpResult, error) {
	l.mu.Lock()
	if r, ok := l.resolved[txid]; ok {
		delete(l.resolved, txid)
		l.mu.Unlock()
		return r, nil
	}
	if l.lastErr != nil {
		err := l.lastErr
		l.mu.Unlock()
		return cpResult{}, err
	}
	ch := make(chan cpResult, 1)
	l.waiters[txid] = ch
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		delete(l.waiters, txid)
		delete(l.resolved, txid)
		l.mu.Unlock()
	}()

	select {
	case r := <-ch:
		return r, nil
	case <-l.down:
		l.mu.Lock()
		defer l.mu.Unlock()
		return cpResult{}, l.lastErr
	case <-time.After(timeout):
		return cpResult{}, fmt.Errorf("%s: %w: tx %s not seen in a block on channel %q from %s within %s",
			"fabric", adapters.ErrFinalityTimeout, txid, l.channel, l.endpoint, timeout)
	case <-ctx.Done():
		return cpResult{}, ctx.Err()
	}
}

func (l *cpListener) close() {
	if l == nil {
		return
	}
	l.cancel()
	<-l.done
	if l.gw != nil {
		l.gw.Close()
	}
	if l.conn != nil {
		l.conn.Close()
	}
}

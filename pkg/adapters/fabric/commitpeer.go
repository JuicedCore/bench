package fabric

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
)

// cpListener watches a Committing Peer's filtered-block event stream and resolves
// transaction finality from there. Drunix's Gateway runs on the Lite Peer, which
// endorses + broadcasts but does NOT commit; its `Commit.Status()` therefore
// never fires. The Committing Peer (a separate node) is where blocks land, so
// finality for Drunix is read from the CP's block events instead.
type cpListener struct {
	conn   *grpc.ClientConn
	gw     *client.Gateway
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	resolved map[string]cpResult          // txid -> outcome (for finality seen before the wait)
	waiters  map[string]chan cpResult     // txid -> signal
}

type cpResult struct {
	blockNum uint64
	valid    bool
	at       time.Time
}

// startCPListener dials the Committing Peer, opens a filtered-block stream on the
// channel, and indexes every transaction's validation code.
func startCPListener(endpoint, tlsCACertPath, serverNameOverride, channel string,
	id *identity.X509Identity, sign identity.Sign) (*cpListener, error) {

	conn, err := dial(endpoint, tlsCACertPath, serverNameOverride)
	if err != nil {
		return nil, fmt.Errorf("cp dial %s: %w", endpoint, err)
	}
	gw, err := client.Connect(id, client.WithSign(sign), client.WithClientConnection(conn))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("cp gateway connect: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	l := &cpListener{
		conn: conn, gw: gw, cancel: cancel, done: make(chan struct{}),
		resolved: map[string]cpResult{}, waiters: map[string]chan cpResult{},
	}

	events, err := gw.GetNetwork(channel).FilteredBlockEvents(ctx)
	if err != nil {
		cancel()
		gw.Close()
		conn.Close()
		return nil, fmt.Errorf("cp filtered block events: %w", err)
	}

	go func() {
		defer close(l.done)
		for {
			select {
			case <-ctx.Done():
				return
			case blk, ok := <-events:
				if !ok {
					return
				}
				num := blk.GetNumber()
				for _, ft := range blk.GetFilteredTransactions() {
					r := cpResult{blockNum: num, valid: ft.GetTxValidationCode() == 0, at: time.Now()}
					l.deliver(ft.GetTxid(), r)
				}
			}
		}
	}()
	return l, nil
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
	}
	l.mu.Unlock()
}

// wait blocks until the CP block stream reports txid, or the timeout elapses.
func (l *cpListener) wait(ctx context.Context, txid string, timeout time.Duration) (cpResult, error) {
	l.mu.Lock()
	if r, ok := l.resolved[txid]; ok {
		delete(l.resolved, txid)
		l.mu.Unlock()
		return r, nil
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
	case <-time.After(timeout):
		return cpResult{}, fmt.Errorf("cp finality timeout for %s", txid)
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

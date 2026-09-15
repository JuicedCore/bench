package fabricx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/juicedcore/bench/pkg/adapters"
)

// blockOutcome is one transaction's terminal result, as observed on the deliver
// stream. observedAt is stamped when the block carrying it was decoded - not
// when a caller got round to asking - so T3 means the same thing here as it does
// for every other adapter (docs/architecture/fairness-guarantees.md).
type blockOutcome struct {
	txID       string
	valid      bool
	code       byte // committerpb.Status when !valid
	blockNum   uint64
	observedAt time.Time
}

// broadcaster submits envelopes to an Arma router and returns the router's own
// acknowledgement for each one. That acknowledgement is the adapter's T2.
//
// Matching replies to envelopes is the whole difficulty. A BroadcastResponse
// carries only a status and an info string - no transaction or request ID - and
// the router answers asynchronously: it spreads requests across several
// router-to-batcher streams by request-ID hash, each on its own goroutine, all
// replying through one channel (fabric-x-orderer node/router/router.go Broadcast,
// shard_router.go Forward). Replies on one client stream can therefore arrive in
// a different order from the envelopes, so matching them by position would
// silently attribute acks to the wrong transactions.
//
// So the broadcaster keeps a pool of streams and never lets a stream carry more
// than one unacknowledged envelope. The next reply on a stream is then
// unambiguously the reply to its envelope. A stream whose reply times out or
// errors is discarded and replaced, because a late reply would otherwise be
// read as the next envelope's.
//
// For an ordinary transaction SUCCESS means the router accepted the envelope and
// forwarded it to a batcher - before ordering and commit. That is the same point
// at which the Fabric gateway's Submit returns, which is why it is the
// comparable T2.
type broadcaster struct {
	conn       *grpc.ClientConn
	client     orderer.AtomicBroadcastClient
	idle       chan *ackStream
	ackTimeout time.Duration
	endpoint   string
	log        *slog.Logger

	// reopenErr is the last failure to replace a broken stream. Submits that
	// then fail on the dead stream report it, so the error says "router
	// unreachable" rather than just "context canceled".
	mu        sync.Mutex
	reopenErr error
}

// ackStream is one broadcast stream with at most one envelope in flight.
type ackStream struct {
	stream  orderer.AtomicBroadcast_BroadcastClient
	cancel  context.CancelFunc
	replies chan ackReply
}

type ackReply struct {
	status common.Status
	info   string
	at     time.Time
	err    error
}

func dialInsecure(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	// The benchmark stack runs with TLS disabled throughout (every service is
	// started with SC_*_TLS_MODE=none), so an insecure channel is correct here
	// rather than a fallback. It is a local single-host benchmark network.
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("fabricx: dial %s: %w", endpoint, err)
	}
	return conn, nil
}

func newBroadcaster(ctx context.Context, endpoint string, streams int, ackTimeout time.Duration) (*broadcaster, error) {
	conn, err := dialInsecure(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if err := waitReady(ctx, conn, endpoint, "broadcast (Arma router)"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	b := &broadcaster{
		conn:       conn,
		client:     orderer.NewAtomicBroadcastClient(conn),
		idle:       make(chan *ackStream, streams),
		ackTimeout: ackTimeout,
		endpoint:   endpoint,
		log:        slog.Default(),
	}
	for i := 0; i < streams; i++ {
		s, err := b.openStream()
		if err != nil {
			b.close()
			return nil, fmt.Errorf("fabricx: open broadcast stream %d/%d to %s: %w", i+1, streams, endpoint, err)
		}
		b.idle <- s
	}
	return b, nil
}

func (b *broadcaster) openStream() (*ackStream, error) {
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := b.client.Broadcast(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	s := &ackStream{stream: stream, cancel: cancel, replies: make(chan ackReply, 1)}
	go func() {
		for {
			resp, err := stream.Recv()
			// Stamp on receipt, not when Submit gets round to reading it.
			r := ackReply{at: time.Now(), err: err}
			if err == nil {
				r.status, r.info = resp.GetStatus(), resp.GetInfo()
			}
			select {
			case s.replies <- r:
			default:
				// A reply with no envelope in flight: the router answered something
				// this stream is no longer waiting for. Nothing to attribute it to.
			}
			if err != nil {
				return
			}
		}
	}()
	return s, nil
}

// submit broadcasts one envelope and waits for the router's reply to it,
// returning when the reply arrived. A non-SUCCESS reply is a rejection by the
// router and comes back as an error.
func (b *broadcaster) submit(ctx context.Context, env *common.Envelope) (time.Time, error) {
	var s *ackStream
	select {
	case s = <-b.idle:
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}

	if err := s.stream.Send(env); err != nil {
		b.replace(s)
		return time.Time{}, b.withReopenErr(fmt.Errorf("fabricx: broadcast send to router %s: %w", b.endpoint, err))
	}

	timer := time.NewTimer(b.ackTimeout)
	defer timer.Stop()
	select {
	case r := <-s.replies:
		if r.err != nil {
			b.replace(s)
			return time.Time{}, b.withReopenErr(fmt.Errorf("fabricx: broadcast stream to router %s: %w", b.endpoint, r.err))
		}
		b.idle <- s
		if r.status != common.Status_SUCCESS {
			return r.at, fmt.Errorf("fabricx: router %s rejected envelope: %s %s", b.endpoint, r.status, r.info)
		}
		return r.at, nil
	case <-timer.C:
		b.replace(s)
		return time.Time{}, fmt.Errorf("fabricx: no acknowledgement from router %s within ack_timeout %s", b.endpoint, b.ackTimeout)
	case <-ctx.Done():
		b.replace(s)
		return time.Time{}, ctx.Err()
	}
}

// replace discards a stream whose state is no longer known and puts a fresh one
// in its place, so the pool never shrinks and no stale reply is misattributed.
func (b *broadcaster) replace(old *ackStream) {
	old.cancel()
	s, err := b.openStream()
	b.mu.Lock()
	prev := b.reopenErr
	b.reopenErr = err
	b.mu.Unlock()
	if err == nil {
		if prev != nil {
			b.log.Info("broadcast stream to router re-established", "router", b.endpoint)
		}
		b.idle <- s
		return
	}
	if prev == nil {
		b.log.Warn("cannot reopen broadcast stream; router may be down (further failures not logged until it recovers)",
			"router", b.endpoint, "err", err)
	}
	// The router is unreachable. Return the dead stream so the pool keeps its
	// size; the next submit on it fails fast and tries to replace it again.
	b.idle <- old
}

// withReopenErr appends the last stream-reopen failure, if any, to err.
func (b *broadcaster) withReopenErr(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reopenErr != nil {
		return fmt.Errorf("%w (router unreachable: %v)", err, b.reopenErr)
	}
	return err
}

// routerSet broadcasts every envelope to all Arma parties' routers, as upstream's
// BFT client does (fabric-x-committer loadgen/adapters/broadcast.go,
// bftBroadcaster.Send).
//
// Sending to one router is not merely less fault tolerant, it is slow: a router
// hands requests to its own party's batcher, and only the shard's primary
// batcher cuts batches. A secondary holds a request until FirstStrikeThreshold
// (10s by default) before forwarding it to the primary, so with a single router
// whose party is not primary every transaction waited ~10s before ordering even
// began - a measured 12s end-to-end latency at 50 TPS. Submitting to every
// router puts each request in the primary's pool straight away; duplicates in
// the secondaries' pools are removed once the batch is ordered.
type routerSet struct {
	routers []*broadcaster
}

func newRouterSet(ctx context.Context, endpoints []string, streams int, ackTimeout time.Duration, log *slog.Logger) (*routerSet, error) {
	rs := &routerSet{}
	for _, ep := range endpoints {
		b, err := newBroadcaster(ctx, ep, streams, ackTimeout)
		if err != nil {
			rs.close()
			return nil, err
		}
		b.log = log
		rs.routers = append(rs.routers, b)
	}
	return rs, nil
}

// submit sends env to every router concurrently and returns when the first one
// acknowledges it with SUCCESS: from then on the envelope is on its way to
// ordering, which is the same T2 point a single-router submit had. It fails only
// if no router accepted it. The remaining sends finish in the background, each
// bounded by ack_timeout, so a slow router cannot hold up the caller.
func (rs *routerSet) submit(ctx context.Context, env *common.Envelope) (time.Time, error) {
	if len(rs.routers) == 1 {
		return rs.routers[0].submit(ctx, env)
	}
	type result struct {
		at  time.Time
		err error
	}
	results := make(chan result, len(rs.routers))
	// Not tied to ctx: the caller returning after the first ack must not cancel
	// the other in-flight sends and churn their streams.
	sendCtx := context.WithoutCancel(ctx)
	for _, b := range rs.routers {
		go func() {
			at, err := b.submit(sendCtx, env)
			results <- result{at, err}
		}()
	}
	var errs []error
	for range rs.routers {
		select {
		case r := <-results:
			if r.err == nil {
				return r.at, nil
			}
			errs = append(errs, r.err)
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		}
	}
	return time.Time{}, fmt.Errorf("fabricx: no router accepted the envelope: %w", errors.Join(errs...))
}

func (rs *routerSet) close() {
	for _, b := range rs.routers {
		b.close()
	}
}

func (b *broadcaster) close() {
	for {
		select {
		case s := <-b.idle:
			s.cancel()
		default:
			if b.conn != nil {
				_ = b.conn.Close()
			}
			return
		}
	}
}

// deliverer reads committed blocks and reports each transaction's outcome. If
// the stream ends mid-run it reconnects and resumes from the block after the
// last one seen; if that keeps failing it marks itself down so waiters fail with
// adapters.ErrFinalityStreamDown instead of timing out one at a time.
type deliverer struct {
	conn     *grpc.ClientConn
	cancel   context.CancelFunc
	done     chan struct{}
	endpoint string
	channel  string
	log      *slog.Logger

	mu      sync.Mutex
	downErr error
	down    chan struct{}
}

// maxDeliverRetries is how many consecutive reconnects may fail to deliver a
// block before the deliverer gives up.
const maxDeliverRetries = 6

// deliverBackoff is the first reconnect delay; it doubles per attempt. A var so
// tests can shorten it.
var deliverBackoff = 250 * time.Millisecond

// newDeliverer connects to the deliver endpoint and starts streaming blocks from
// the newest onward, invoking onBlock for every transaction in each block. It
// waits up to ctx's deadline for the first reply so that a wrong channel_id or an
// unreachable sidecar fails Setup rather than every transaction later.
func newDeliverer(ctx context.Context, endpoint, channelID string, onBlock func([]blockOutcome), log *slog.Logger) (*deliverer, error) {
	conn, err := dialInsecure(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if err := waitReady(ctx, conn, endpoint, "deliver (sidecar)"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.Background())
	d := &deliverer{
		conn: conn, cancel: cancel, done: make(chan struct{}), down: make(chan struct{}),
		endpoint: endpoint, channel: channelID,
		log: log.With("stream", "deliver", "endpoint", endpoint, "channel", channelID),
	}
	first := make(chan error, 1)
	go d.run(sctx, onBlock, first)

	select {
	case err := <-first:
		if err != nil {
			d.close()
			return nil, err
		}
	case <-ctx.Done():
		// No block yet (an idle ledger may not send one); the stream is open, so
		// carry on - a later failure is still detected and reported.
		d.log.Warn("no reply from the deliver stream during setup; continuing", "waited", "dial_timeout")
	}
	return d, nil
}

// run owns the deliver stream. first receives the outcome of the first
// subscription: nil once a block arrives, or the error that ended it.
func (d *deliverer) run(ctx context.Context, onBlock func([]blockOutcome), first chan<- error) {
	defer close(d.done)
	report := func(err error) {
		if first != nil {
			first <- err
			first = nil
		}
	}
	var next uint64
	sawBlock := false
	failures := 0
	for {
		err := d.stream(ctx, next, sawBlock, func(blk *common.Block) {
			if !sawBlock || failures > 0 {
				report(nil)
			}
			sawBlock, failures = true, 0
			next = blk.GetHeader().GetNumber() + 1
			// One stamp per block: this is T3 for every transaction it carries.
			if outs := decodeBlock(blk, time.Now()); len(outs) > 0 {
				onBlock(outs)
			}
		})
		if ctx.Err() != nil {
			report(nil)
			return
		}
		// A rejected seek on the very first subscription is a configuration
		// error: fail Setup with it instead of retrying.
		if first != nil && errors.Is(err, errDeliverRejected) {
			report(err)
			return
		}
		failures++
		if failures > maxDeliverRetries {
			down := fmt.Errorf("%w: fabricx deliver stream from %s (channel %q) failed %d times in a row, last: %v",
				adapters.ErrFinalityStreamDown, d.endpoint, d.channel, failures, err)
			d.mu.Lock()
			d.downErr = down
			close(d.down)
			d.mu.Unlock()
			d.log.Error("finality stream is down; every pending and future transaction on this run will fail", "err", down)
			report(down)
			return
		}
		backoff := time.Duration(1<<(failures-1)) * deliverBackoff
		d.log.Warn("deliver stream ended; reconnecting", "err", err, "attempt", failures, "backoff", backoff, "resume_block", next)
		select {
		case <-ctx.Done():
			report(nil)
			return
		case <-time.After(backoff):
		}
	}
}

// errDeliverRejected marks a deliver stream the server answered with a
// non-success status instead of blocks.
var errDeliverRejected = errors.New("deliver request rejected")

// stream runs one deliver subscription until it ends, returning why.
//
// The sidecar serves the peer Deliver service (service/sidecar/sidecar.go
// registers peer.RegisterDeliverServer), not the orderer's AtomicBroadcast: its
// blocks carry the committer's per-transaction statuses, which the orderer's do
// not. Calling orderer.AtomicBroadcast/Deliver here fails with Unimplemented.
func (d *deliverer) stream(ctx context.Context, from uint64, specified bool, onBlock func(*common.Block)) error {
	stream, err := peer.NewDeliverClient(d.conn).Deliver(ctx)
	if err != nil {
		return fmt.Errorf("open deliver stream: %w", err)
	}
	seek, err := seekEnvelope(d.channel, from, specified)
	if err != nil {
		return err
	}
	if err := stream.Send(seek); err != nil {
		return fmt.Errorf("send seek: %w", err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("server closed the deliver stream")
			}
			return fmt.Errorf("recv: %w", err)
		}
		if blk := resp.GetBlock(); blk != nil {
			onBlock(blk)
			continue
		}
		if st := resp.GetStatus(); st != common.Status_UNKNOWN {
			// A status reply ends the stream. SUCCESS only follows a bounded seek,
			// which this never sends; anything else is the server refusing it.
			return fmt.Errorf("%w by %s: status %s for channel %q (wrong adapter.channel_id? armageddon uses \"arma\")",
				errDeliverRejected, d.endpoint, st, d.channel)
		}
	}
}

// err returns the reason the stream is down, or nil.
func (d *deliverer) err() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.downErr
}

func (d *deliverer) close() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.conn != nil {
		_ = d.conn.Close()
	}
	if d.done != nil {
		<-d.done
	}
}

// waitReady connects conn and waits until it is READY or ctx ends. grpc.NewClient
// is lazy, so without this an unreachable endpoint only shows up as the first
// transaction's failure.
func waitReady(ctx context.Context, conn *grpc.ClientConn, endpoint, what string) error {
	conn.Connect()
	for {
		st := conn.GetState()
		switch st {
		case connectivity.Ready:
			return nil
		case connectivity.Shutdown:
			return fmt.Errorf("fabricx: %s connection to %s shut down", what, endpoint)
		}
		if !conn.WaitForStateChange(ctx, st) {
			return fmt.Errorf("fabricx: %s endpoint %s not reachable within the dial timeout (last state %s): is the fabricx stack up (docker ps | grep fabricx) and the endpoint in connection.env right?",
				what, endpoint, st)
		}
	}
}

// seekEnvelope asks for blocks from the newest (first subscription) or from a
// specific block (resuming after a reconnect) to the end of time.
func seekEnvelope(channelID string, from uint64, specified bool) (*common.Envelope, error) {
	start := &orderer.SeekPosition{Type: &orderer.SeekPosition_Newest{Newest: &orderer.SeekNewest{}}}
	if specified {
		start = &orderer.SeekPosition{Type: &orderer.SeekPosition_Specified{Specified: &orderer.SeekSpecified{Number: from}}}
	}
	seekInfo := &orderer.SeekInfo{
		Start:    start,
		Stop:     &orderer.SeekPosition{Type: &orderer.SeekPosition_Specified{Specified: &orderer.SeekSpecified{Number: ^uint64(0)}}},
		Behavior: orderer.SeekInfo_BLOCK_UNTIL_READY,
	}
	hdr, err := proto.Marshal(&common.ChannelHeader{
		Type:      int32(common.HeaderType_DELIVER_SEEK_INFO),
		ChannelId: channelID,
	})
	if err != nil {
		return nil, err
	}
	data, err := proto.Marshal(seekInfo)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&common.Payload{
		Header: &common.Header{ChannelHeader: hdr},
		Data:   data,
	})
	if err != nil {
		return nil, err
	}
	return &common.Envelope{Payload: payload}, nil
}

// decodeBlock pairs each transaction's ID with its validation code.
//
// The per-transaction codes live in the block's TRANSACTIONS_FILTER metadata,
// positionally aligned with block.Data.Data. They are committerpb.Status values
// (the sidecar writes byte(status) in service/sidecar/mapping.go), NOT Fabric's
// peer.TxValidationCode: COMMITTED is 1 and 0 means "not validated yet". Reading
// them with Fabric's 0 == VALID convention marks every committed transaction
// invalid. A transaction whose status is not COMMITTED is a platform verdict, not
// a transport error, and is reported as such.
func decodeBlock(blk *common.Block, observedAt time.Time) []blockOutcome {
	if blk.GetData() == nil {
		return nil
	}
	txs := blk.GetData().GetData()
	var codes []byte
	if md := blk.GetMetadata().GetMetadata(); len(md) > int(common.BlockMetadataIndex_TRANSACTIONS_FILTER) {
		codes = md[common.BlockMetadataIndex_TRANSACTIONS_FILTER]
	}

	out := make([]blockOutcome, 0, len(txs))
	for i, raw := range txs {
		txID, err := envelopeTxID(raw)
		if err != nil || txID == "" {
			continue
		}
		valid := true
		var code byte
		if i < len(codes) {
			code = codes[i]
			valid = committerpb.Status(code) == committerpb.Status_COMMITTED
		}
		out = append(out, blockOutcome{
			txID:       txID,
			valid:      valid,
			code:       code,
			blockNum:   blk.GetHeader().GetNumber(),
			observedAt: observedAt,
		})
	}
	return out
}

func envelopeTxID(raw []byte) (string, error) {
	var env common.Envelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		return "", err
	}
	var payload common.Payload
	if err := proto.Unmarshal(env.GetPayload(), &payload); err != nil {
		return "", err
	}
	var ch common.ChannelHeader
	if err := proto.Unmarshal(payload.GetHeader().GetChannelHeader(), &ch); err != nil {
		return "", err
	}
	return ch.GetTxId(), nil
}

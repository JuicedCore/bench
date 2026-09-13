package fabricx

import (
	"context"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// blockOutcome is one transaction's terminal result, as observed on the deliver
// stream. observedAt is stamped when the block carrying it was decoded - not
// when a caller got round to asking - so T3 means the same thing here as it does
// for every other adapter (docs/architecture/fairness-guarantees.md).
type blockOutcome struct {
	txID       string
	valid      bool
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
	b := &broadcaster{
		conn:       conn,
		client:     orderer.NewAtomicBroadcastClient(conn),
		idle:       make(chan *ackStream, streams),
		ackTimeout: ackTimeout,
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
		return time.Time{}, fmt.Errorf("fabricx: broadcast send: %w", err)
	}

	timer := time.NewTimer(b.ackTimeout)
	defer timer.Stop()
	select {
	case r := <-s.replies:
		if r.err != nil {
			b.replace(s)
			return time.Time{}, fmt.Errorf("fabricx: broadcast stream: %w", r.err)
		}
		b.idle <- s
		if r.status != common.Status_SUCCESS {
			return r.at, fmt.Errorf("fabricx: router rejected envelope: %s %s", r.status, r.info)
		}
		return r.at, nil
	case <-timer.C:
		b.replace(s)
		return time.Time{}, fmt.Errorf("fabricx: no router acknowledgement within %s", b.ackTimeout)
	case <-ctx.Done():
		b.replace(s)
		return time.Time{}, ctx.Err()
	}
}

// replace discards a stream whose state is no longer known and puts a fresh one
// in its place, so the pool never shrinks and no stale reply is misattributed.
func (b *broadcaster) replace(old *ackStream) {
	old.cancel()
	if s, err := b.openStream(); err == nil {
		b.idle <- s
		return
	}
	// The router is unreachable. Return the dead stream so the pool keeps its
	// size; the next submit on it fails fast and tries to replace it again.
	b.idle <- old
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

// deliverer reads committed blocks and reports each transaction's outcome.
type deliverer struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc
	done   chan struct{}
}

// newDeliverer starts streaming blocks from the newest onward, invoking onBlock
// for every transaction in each block.
func newDeliverer(endpoint, channelID string, onBlock func([]blockOutcome)) (*deliverer, error) {
	conn, err := dialInsecure(context.Background(), endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := orderer.NewAtomicBroadcastClient(conn).Deliver(ctx)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("fabricx: open deliver stream to %s: %w", endpoint, err)
	}
	// Seek from the next block to the end of time: we only care about
	// transactions this run submits, not ledger history.
	seek, err := seekEnvelope(channelID)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	if err := stream.Send(seek); err != nil {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("fabricx: send seek: %w", err)
	}

	d := &deliverer{conn: conn, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			blk := resp.GetBlock()
			if blk == nil {
				continue
			}
			// One stamp per block: this is T3 for every transaction it carries.
			if outs := decodeBlock(blk, time.Now()); len(outs) > 0 {
				onBlock(outs)
			}
		}
	}()
	return d, nil
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

func seekEnvelope(channelID string) (*common.Envelope, error) {
	seekInfo := &orderer.SeekInfo{
		Start:    &orderer.SeekPosition{Type: &orderer.SeekPosition_Newest{Newest: &orderer.SeekNewest{}}},
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
// positionally aligned with block.Data.Data. A transaction whose code is not
// VALID committed to the ledger as invalid - that is a platform verdict, not a
// transport error, and is reported as such.
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
		if i < len(codes) {
			valid = codes[i] == 0 // 0 == VALID
		}
		out = append(out, blockOutcome{
			txID:       txID,
			valid:      valid,
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

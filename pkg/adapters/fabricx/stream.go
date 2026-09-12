package fabricx

import (
	"context"
	"fmt"
	"sync"
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

// broadcaster submits envelopes to an Arma router.
//
// Broadcast returns an ack once the router has accepted the envelope for
// ordering, which is strictly before commit - that ack is the adapter's T2.
type broadcaster struct {
	conn   *grpc.ClientConn
	stream orderer.AtomicBroadcast_BroadcastClient

	// sendMu serialises Send: a gRPC stream supports one concurrent sender, but
	// the harness drives Submit from many goroutines.
	sendMu sync.Mutex
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

func newBroadcaster(ctx context.Context, endpoint string) (*broadcaster, error) {
	conn, err := dialInsecure(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	stream, err := orderer.NewAtomicBroadcastClient(conn).Broadcast(context.Background())
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("fabricx: open broadcast stream to %s: %w", endpoint, err)
	}
	b := &broadcaster{conn: conn, stream: stream}
	// Drain acks so the stream's flow control does not stall. A non-SUCCESS
	// status means the router refused the envelope outright; the transaction
	// will then simply never appear on the deliver stream and time out, which
	// the run's failure breakdown reports.
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()
	return b, nil
}

func (b *broadcaster) send(env *common.Envelope) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return b.stream.Send(env)
}

func (b *broadcaster) close() {
	if b.stream != nil {
		_ = b.stream.CloseSend()
	}
	if b.conn != nil {
		_ = b.conn.Close()
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

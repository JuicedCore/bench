package neuchain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-zeromq/zmq4"
	"google.golang.org/protobuf/proto"

	pb "github.com/juicedcore/bench/pkg/adapters/neuchain/proto"
)

// zmqTransport holds the ZeroMQ sockets: one PUB per block server for submit
// (NeuChain SUBs on :5001), and one REQ for tip/block queries (NeuChain REPs on
// :7003).
type zmqTransport struct {
	ctx    context.Context
	cancel context.CancelFunc

	pub  []zmq4.Socket
	rr   atomic.Uint64
	qmu  sync.Mutex // REQ sockets are strict req/rep - serialise
	req  zmq4.Socket
	sign func([]byte) ([]byte, error)
}

func newTransport(parent context.Context, cfg *Config, sign func([]byte) ([]byte, error)) (*zmqTransport, error) {
	ctx, cancel := context.WithCancel(parent)
	t := &zmqTransport{ctx: ctx, cancel: cancel, sign: sign}

	for _, ep := range cfg.BlockServers {
		s := zmq4.NewPub(ctx)
		if err := s.Dial(tcp(ep)); err != nil {
			t.Close()
			return nil, fmt.Errorf("neuchain: dial submit %s: %w", ep, err)
		}
		t.pub = append(t.pub, s)
	}

	t.req = zmq4.NewReq(ctx)
	if err := t.req.Dial(tcp(cfg.QueryEndpoint)); err != nil {
		t.Close()
		return nil, fmt.Errorf("neuchain: dial query %s: %w", cfg.QueryEndpoint, err)
	}

	// PUB is a slow joiner: give the SUB side a moment to complete the
	// subscription handshake before the first publish, or early txs are dropped.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
	}
	return t, nil
}

func (t *zmqTransport) Close() {
	if t.cancel != nil {
		t.cancel()
	}
	for _, s := range t.pub {
		_ = s.Close()
	}
	if t.req != nil {
		_ = t.req.Close()
	}
}

// publish sends the serialized UserRequest to one block server (round-robin).
// NeuChain's deterministic execution means every server must ultimately see
// every tx; a PUB fan-out to all SUBs on the far side achieves that, so sending
// to one endpoint's PUB socket is sufficient per submit.
func (t *zmqTransport) publish(wire []byte) error {
	if len(t.pub) == 0 {
		return fmt.Errorf("neuchain: no submit sockets")
	}
	i := int(t.rr.Add(1)-1) % len(t.pub)
	return t.pub[i].Send(zmq4.NewMsg(wire))
}

// query sends a signed UserQueryRequest and returns the raw reply frame.
func (t *zmqTransport) query(qtype, payload string) ([]byte, error) {
	sig, err := t.sign([]byte(payload))
	if err != nil {
		return nil, err
	}
	reqRaw, err := proto.Marshal(&pb.UserQueryRequest{
		Type: []byte(qtype), Payload: []byte(payload), Digest: sig,
	})
	if err != nil {
		return nil, err
	}
	t.qmu.Lock()
	defer t.qmu.Unlock()
	if err := t.req.Send(zmq4.NewMsg(reqRaw)); err != nil {
		return nil, fmt.Errorf("neuchain: query send: %w", err)
	}
	msg, err := t.req.Recv()
	if err != nil {
		return nil, fmt.Errorf("neuchain: query recv: %w", err)
	}
	if len(msg.Frames) == 0 {
		return nil, fmt.Errorf("neuchain: empty query reply")
	}
	return msg.Frames[0], nil
}

// tip returns the latest committed block height (0 if none yet).
func (t *zmqTransport) tip() (uint64, error) {
	raw, err := t.query("tip_query", "")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("neuchain: bad tip reply %q: %w", raw, err)
	}
	return n, nil
}

// block fetches block n and returns its decoded result frames.
func (t *zmqTransport) block(n uint64) ([]resultFrame, error) {
	raw, err := t.query("block_query", strconv.FormatUint(n, 10))
	if err != nil {
		return nil, err
	}
	var blk pb.Block
	if err := proto.Unmarshal(raw, &blk); err != nil {
		return nil, fmt.Errorf("neuchain: unmarshal block %d: %w", n, err)
	}
	if blk.GetData() == nil {
		return nil, nil
	}
	frames := make([]resultFrame, 0, len(blk.GetData().GetData()))
	for _, entry := range blk.GetData().GetData() {
		f, err := decodeResultFrame(entry)
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}
	return frames, nil
}

func tcp(hostPort string) string {
	if strings.Contains(hostPort, "://") {
		return hostPort
	}
	return "tcp://" + hostPort
}

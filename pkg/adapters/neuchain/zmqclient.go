package neuchain

import (
	"context"
	"errors"
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

	queryEP string
	pubEP   []string
}

func newTransport(parent context.Context, cfg *Config, sign func([]byte) ([]byte, error)) (*zmqTransport, error) {
	ctx, cancel := context.WithCancel(parent)
	t := &zmqTransport{ctx: ctx, cancel: cancel, sign: sign}

	// Explicit timeouts and reconnect: zmq4's defaults are a 5 minute socket
	// timeout and no reconnect, so a block-server restart would silently kill
	// submits and block the finality poller for minutes at a time.
	opts := []zmq4.Option{
		zmq4.WithTimeout(cfg.SocketTimeout),
		zmq4.WithDialerTimeout(cfg.DialTimeout),
		zmq4.WithDialerRetry(250 * time.Millisecond),
		zmq4.WithAutomaticReconnect(true),
	}
	for _, ep := range cfg.BlockServers {
		s, err := dialZMQ(ctx, func() zmq4.Socket { return zmq4.NewPub(ctx, opts...) }, tcp(ep), cfg.DialTimeout)
		if err != nil {
			t.Close()
			return nil, fmt.Errorf("neuchain: dial block server submit socket %s: %w (is the neuchain stack up? docker ps | grep block-server)", ep, err)
		}
		t.pub = append(t.pub, s)
		t.pubEP = append(t.pubEP, ep)
	}

	req, err := dialZMQ(ctx, func() zmq4.Socket { return zmq4.NewReq(ctx, opts...) }, tcp(cfg.QueryEndpoint), cfg.DialTimeout)
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("neuchain: dial query socket %s: %w (the block server's query port is normally 7003)", cfg.QueryEndpoint, err)
	}
	t.req = req
	t.queryEP = cfg.QueryEndpoint

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
//
// PUB is fire-and-forget: a nil error means the message was queued, not that a
// block server received it. A dropped message surfaces as a finality timeout.
func (t *zmqTransport) publish(wire []byte) error {
	if len(t.pub) == 0 {
		return fmt.Errorf("neuchain: no submit sockets")
	}
	i := int(t.rr.Add(1)-1) % len(t.pub)
	if err := t.pub[i].Send(zmq4.NewMsg(wire)); err != nil {
		return fmt.Errorf("neuchain: publish to block server %s: %w", t.pubEP[i], err)
	}
	return nil
}

// publishTo sends to block server i, bypassing the round-robin.
func (t *zmqTransport) publishTo(i int, wire []byte) error {
	if err := t.pub[i].Send(zmq4.NewMsg(wire)); err != nil {
		return fmt.Errorf("neuchain: publish to block server %s: %w", t.pubEP[i], err)
	}
	return nil
}

// query sends a signed UserQueryRequest and returns the raw reply frame.
func (t *zmqTransport) query(qtype, payload string) ([]byte, error) {
	sig, err := t.sign([]byte(payload))
	if err != nil {
		return nil, fmt.Errorf("neuchain: sign %s: %w", qtype, err)
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
		return nil, fmt.Errorf("neuchain: %s send to %s: %w", qtype, t.queryEP, err)
	}
	msg, err := t.req.Recv()
	if err != nil {
		return nil, fmt.Errorf("neuchain: %s reply from %s: %w", qtype, t.queryEP, err)
	}
	if len(msg.Frames) == 0 {
		return nil, fmt.Errorf("neuchain: empty %s reply from %s", qtype, t.queryEP)
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
		// Either a genuinely empty block or one the server could not return yet;
		// the poller retries, then skips it with a log line.
		return nil, errEmptyBlock
	}
	frames := make([]resultFrame, 0, len(blk.GetData().GetData()))
	for _, entry := range blk.GetData().GetData() {
		f, err := decodeResultFrame(entry)
		if err != nil {
			return frames, fmt.Errorf("neuchain: block %d result frame %d: %w", n, len(frames), err)
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// errEmptyBlock marks a block_query reply with no data section.
var errEmptyBlock = errors.New("block has no data")

func tcp(hostPort string) string {
	if strings.Contains(hostPort, "://") {
		return hostPort
	}
	return "tcp://" + hostPort
}

// dialZMQ retries Dial until deadline. NeuChain's SUB/REP sockets can RST a
// ZMTP greeting for a few seconds after the TCP port is already open.
func dialZMQ(ctx context.Context, newSock func() zmq4.Socket, addr string, deadline time.Duration) (zmq4.Socket, error) {
	if deadline <= 0 {
		deadline = 10 * time.Second
	}
	end := time.Now().Add(deadline)
	var last error
	for {
		s := newSock()
		err := s.Dial(addr)
		if err == nil {
			return s, nil
		}
		last = err
		_ = s.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !time.Now().Before(end) {
			return nil, last
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

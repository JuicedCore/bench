package fabricx

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"google.golang.org/grpc"
)

// fakeRouter stands in for an Arma router. Each envelope's payload tells it how
// to answer: "ok:<ms>" replies SUCCESS after that delay, "reject" replies with an
// error status, "hang" never replies. Replies are sent asynchronously, like the
// real router, so they could leave in a different order from the envelopes if a
// client ever put two on one stream - which is what overlap records.
type fakeRouter struct {
	orderer.UnimplementedAtomicBroadcastServer
	overlap   atomic.Int32 // times a stream had >1 envelope awaiting a reply
	streams   atomic.Int32
	rejectAll atomic.Bool  // answer every envelope "reject", whatever it says
	received  atomic.Int32 // envelopes received
}

func (f *fakeRouter) Broadcast(stream orderer.AtomicBroadcast_BroadcastServer) error {
	f.streams.Add(1)
	var sendMu sync.Mutex
	var outstanding atomic.Int32
	for {
		env, err := stream.Recv()
		if err != nil {
			return nil
		}
		if outstanding.Add(1) > 1 {
			f.overlap.Add(1)
		}
		f.received.Add(1)
		cmd := string(env.GetPayload())
		if f.rejectAll.Load() {
			cmd = "reject"
		}
		go func() {
			switch {
			case cmd == "hang":
				return // never answered, never released
			case cmd == "reject":
				outstanding.Add(-1)
				sendMu.Lock()
				_ = stream.Send(&orderer.BroadcastResponse{Status: common.Status_BAD_REQUEST, Info: "nope"})
				sendMu.Unlock()
			case strings.HasPrefix(cmd, "ok:"):
				ms, _ := strconv.Atoi(strings.TrimPrefix(cmd, "ok:"))
				time.Sleep(time.Duration(ms) * time.Millisecond)
				outstanding.Add(-1)
				sendMu.Lock()
				_ = stream.Send(&orderer.BroadcastResponse{Status: common.Status_SUCCESS})
				sendMu.Unlock()
			}
		}()
	}
}

func (f *fakeRouter) Deliver(orderer.AtomicBroadcast_DeliverServer) error { return nil }

func startFakeRouter(t *testing.T) (*fakeRouter, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	f := &fakeRouter{}
	orderer.RegisterAtomicBroadcastServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return f, lis.Addr().String()
}

func env(cmd string) *common.Envelope { return &common.Envelope{Payload: []byte(cmd)} }

// T2 must be when the router's reply arrived, not when the envelope left.
func TestBroadcastAckTimeIsTheRoutersReply(t *testing.T) {
	_, addr := startFakeRouter(t)
	b, err := newBroadcaster(context.Background(), addr, 4, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	start := time.Now()
	ackAt, err := b.submit(context.Background(), env("ok:120"))
	if err != nil {
		t.Fatal(err)
	}
	if d := ackAt.Sub(start); d < 110*time.Millisecond {
		t.Errorf("ack stamped %s after send; the router took 120ms, so this is a send return, not its reply", d)
	}
}

func TestBroadcastRouterRejectionIsAnError(t *testing.T) {
	_, addr := startFakeRouter(t)
	b, err := newBroadcaster(context.Background(), addr, 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if _, err := b.submit(context.Background(), env("reject")); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("router rejection not reported: %v", err)
	}
	// The stream is still usable afterwards.
	if _, err := b.submit(context.Background(), env("ok:1")); err != nil {
		t.Errorf("stream unusable after a rejection: %v", err)
	}
}

// Many concurrent submits with deliberately mixed delays. Every one must get its
// own reply's timing, and no stream may ever carry two unacknowledged envelopes -
// the router's replies carry no ID, so that is the only thing keeping ack
// attribution correct.
func TestBroadcastConcurrentSubmitsNeverShareAStream(t *testing.T) {
	f, addr := startFakeRouter(t)
	b, err := newBroadcaster(context.Background(), addr, 8, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	delays := []int{80, 5, 40, 1, 60, 10, 30, 2}
	var wg sync.WaitGroup
	var bad atomic.Int32
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(ms int) {
			defer wg.Done()
			start := time.Now()
			ackAt, err := b.submit(context.Background(), env("ok:"+strconv.Itoa(ms)))
			if err != nil || ackAt.Sub(start) < time.Duration(ms)*time.Millisecond {
				bad.Add(1)
			}
		}(delays[i%len(delays)])
	}
	wg.Wait()
	if n := bad.Load(); n > 0 {
		t.Errorf("%d submits returned before their own reply could have arrived (ack misattributed)", n)
	}
	if n := f.overlap.Load(); n > 0 {
		t.Errorf("a stream carried more than one unacknowledged envelope %d time(s)", n)
	}
}

// A reply that never comes leaves the stream's state unknown: a late reply would
// otherwise be read as the next envelope's ack. The stream must be replaced, and
// the pool must keep working.
func TestBroadcastTimeoutReplacesTheStream(t *testing.T) {
	f, addr := startFakeRouter(t)
	b, err := newBroadcaster(context.Background(), addr, 1, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	if _, err := b.submit(context.Background(), env("hang")); err == nil {
		t.Fatal("expected a timeout for an unanswered envelope")
	}
	// Pool size 1: this only succeeds if the hung stream was replaced.
	if _, err := b.submit(context.Background(), env("ok:1")); err != nil {
		t.Fatalf("broadcaster stuck after a timeout: %v", err)
	}
	if n := f.streams.Load(); n < 2 {
		t.Errorf("router saw %d stream(s); the timed-out stream should have been replaced", n)
	}
	if n := f.overlap.Load(); n > 0 {
		t.Errorf("replacement reused the hung stream: overlap=%d", n)
	}
}

// Every router gets the envelope, and one accepting is enough: a secondary
// party's router refusing must not fail a transaction the primary's took.
func TestRouterSetSendsToAllAndSucceedsOnAnyAck(t *testing.T) {
	bad, badAddr := startFakeRouter(t)
	bad.rejectAll.Store(true)
	good, goodAddr := startFakeRouter(t)
	rs, err := newRouterSet(context.Background(), []string{badAddr, goodAddr}, 2, 5*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer rs.close()

	if _, err := rs.submit(context.Background(), env("ok:0")); err != nil {
		t.Fatalf("one router accepted, want success, got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (bad.received.Load() == 0 || good.received.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if bad.received.Load() != 1 || good.received.Load() != 1 {
		t.Errorf("received bad=%d good=%d, want 1 each", bad.received.Load(), good.received.Load())
	}
}

func TestRouterSetFailsWhenNoRouterAccepts(t *testing.T) {
	a, aAddr := startFakeRouter(t)
	b, bAddr := startFakeRouter(t)
	a.rejectAll.Store(true)
	b.rejectAll.Store(true)
	rs, err := newRouterSet(context.Background(), []string{aAddr, bAddr}, 1, 5*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer rs.close()
	if _, err := rs.submit(context.Background(), env("ok:0")); err == nil || !strings.Contains(err.Error(), "no router accepted") {
		t.Fatalf("want no-router-accepted error, got %v", err)
	}
}

package fabricx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc"

	"github.com/juicedcore/bench/pkg/adapters"
)

// statusDeliver stands in for the sidecar's peer Deliver service. It answers every deliver request with a fixed status (no blocks),
// or, with status UNKNOWN, closes the stream immediately.
type statusDeliver struct {
	peer.UnimplementedDeliverServer
	status common.Status
}

func (s *statusDeliver) Deliver(srv peer.Deliver_DeliverServer) error {
	if _, err := srv.Recv(); err != nil {
		return err
	}
	if s.status == common.Status_UNKNOWN {
		return nil
	}
	return srv.Send(&peer.DeliverResponse{Type: &peer.DeliverResponse_Status{Status: s.status}})
}

func startDeliver(t *testing.T, st common.Status) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	peer.RegisterDeliverServer(srv, &statusDeliver{status: st})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestDeliverRejectedSeekFailsSetupWithChannelHint(t *testing.T) {
	addr := startDeliver(t, common.Status_NOT_FOUND)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := newDeliverer(ctx, addr, "wrong-channel", func([]blockOutcome) {}, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "NOT_FOUND") || !strings.Contains(err.Error(), "channel_id") {
		t.Fatalf("want a NOT_FOUND error naming channel_id, got %v", err)
	}
}

func TestDeliverUnreachableEndpointFailsWithinDialTimeout(t *testing.T) {
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	_ = lis.Close() // nothing listens here now
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newDeliverer(ctx, addr, "arma", func([]blockOutcome) {}, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("want not-reachable error, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("dial timeout not honoured")
	}
}

func TestDeliverStreamThatKeepsClosingGoesDown(t *testing.T) {
	old := deliverBackoff
	deliverBackoff = time.Millisecond
	defer func() { deliverBackoff = old }()

	addr := startDeliver(t, common.Status_UNKNOWN)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	d, err := newDeliverer(ctx, addr, "arma", func([]blockOutcome) {}, slog.Default())
	if err != nil && !errors.Is(err, adapters.ErrFinalityStreamDown) {
		t.Fatalf("unexpected setup error: %v", err)
	}
	if d == nil {
		return // gave up during setup: also acceptable, and already reported
	}
	defer d.close()
	select {
	case <-d.down:
	case <-time.After(5 * time.Second):
		t.Fatal("deliverer never marked itself down")
	}
	if !errors.Is(d.err(), adapters.ErrFinalityStreamDown) {
		t.Errorf("err = %v, want ErrFinalityStreamDown", d.err())
	}
}

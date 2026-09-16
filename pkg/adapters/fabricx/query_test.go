package fabricx

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/juicedcore/bench/pkg/adapters"
)

// fakeQuery is a QueryService that answers GetRows from an in-memory map and
// GetNamespacePolicies with an empty policy set (enough for newQuerier's ping).
type fakeQuery struct {
	committerpb.UnimplementedQueryServiceServer
	rows map[string][]byte // key -> value
}

func (f *fakeQuery) GetNamespacePolicies(context.Context, *emptypb.Empty) (*applicationpb.NamespacePolicies, error) {
	return &applicationpb.NamespacePolicies{}, nil
}

func (f *fakeQuery) GetRows(_ context.Context, q *committerpb.Query) (*committerpb.Rows, error) {
	out := &committerpb.Rows{}
	for _, ns := range q.GetNamespaces() {
		rn := &committerpb.RowsNamespace{NsId: ns.GetNsId()}
		for _, k := range ns.GetKeys() {
			if v, ok := f.rows[string(k)]; ok {
				rn.Rows = append(rn.Rows, &committerpb.Row{Key: k, Value: v, Version: 1})
			}
		}
		out.Namespaces = append(out.Namespaces, rn)
	}
	return out, nil
}

func startQuery(t *testing.T, rows map[string][]byte) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	committerpb.RegisterQueryServiceServer(srv, &fakeQuery{rows: rows})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestQueryGetRowsReturnsValueAndMissing(t *testing.T) {
	addr := startQuery(t, map[string][]byte{"key-1": []byte("hello")})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q, err := newQuerier(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	v, found, err := q.get(ctx, "0", "key-1")
	if err != nil || !found || string(v) != "hello" {
		t.Fatalf("get key-1: val=%q found=%v err=%v", v, found, err)
	}
	v, found, err = q.get(ctx, "0", "absent")
	if err != nil || found || v != nil {
		t.Fatalf("get absent: val=%q found=%v err=%v", v, found, err)
	}
}

func TestSubmitReadUsesQueryNotEnvelope(t *testing.T) {
	addr := startQuery(t, map[string][]byte{"k": []byte("v")})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	q, err := newQuerier(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)

	a := &Adapter{
		cfg:   &Config{Namespace: "0"},
		qc:    q,
		reads: map[string]struct{}{},
	}
	res, err := a.Submit(ctx, &adapters.Transaction{Kind: adapters.TxRead, Key: "k", Seq: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.TxID == "" || res.AckTime.IsZero() {
		t.Fatalf("submit result incomplete: %+v", res)
	}
	fr, err := a.WaitForFinality(ctx, res.TxID, time.Second)
	if err != nil || !fr.Valid {
		t.Fatalf("finality: %+v err=%v", fr, err)
	}

	qr, err := a.Query(ctx, "k")
	if err != nil || !qr.Found || string(qr.Value) != "v" {
		t.Fatalf("Query: %+v err=%v", qr, err)
	}
}

func TestSubmitReadWithoutQueryEndpointFails(t *testing.T) {
	a := &Adapter{cfg: &Config{Namespace: "0"}}
	_, err := a.Submit(context.Background(), &adapters.Transaction{Kind: adapters.TxRead, Key: "k"})
	if err == nil || !strings.Contains(err.Error(), "query service not configured") {
		t.Fatalf("want query-not-configured error, got %v", err)
	}
}

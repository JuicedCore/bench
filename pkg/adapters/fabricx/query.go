package fabricx

import (
	"context"
	"fmt"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// querier is the Fabric-X QueryService client: a point lookup against committed
// world state (PostgreSQL behind the query process). It does not go through
// Arma. Omitting Query.view reads the latest committed snapshot, which is the
// default client behaviour (see fabric-x-common QueryService docs).
type querier struct {
	conn     *grpc.ClientConn
	client   committerpb.QueryServiceClient
	endpoint string
}

func newQuerier(ctx context.Context, endpoint string) (*querier, error) {
	conn, err := dialInsecure(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if err := waitReady(ctx, conn, endpoint, "query"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	q := &querier{
		conn:     conn,
		client:   committerpb.NewQueryServiceClient(conn),
		endpoint: endpoint,
	}
	if _, err := q.client.GetNamespacePolicies(ctx, &emptypb.Empty{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("fabricx: query service at %s did not answer GetNamespacePolicies: %w (is :7001 published? docker compose logs fabricx-committer)", endpoint, err)
	}
	return q, nil
}

func (q *querier) close() {
	if q == nil || q.conn == nil {
		return
	}
	_ = q.conn.Close()
}

func (q *querier) get(ctx context.Context, ns, key string) ([]byte, bool, error) {
	if q == nil || q.client == nil {
		return nil, false, fmt.Errorf("fabricx: query service not connected")
	}
	rows, err := q.client.GetRows(ctx, &committerpb.Query{
		Namespaces: []*committerpb.QueryNamespace{{
			NsId: ns,
			Keys: [][]byte{[]byte(key)},
		}},
	})
	if err != nil {
		return nil, false, fmt.Errorf("fabricx: GetRows %s/%s via %s: %w", ns, key, q.endpoint, err)
	}
	want := []byte(key)
	for _, n := range rows.GetNamespaces() {
		for _, r := range n.GetRows() {
			if string(r.GetKey()) == string(want) {
				return r.GetValue(), true, nil
			}
		}
	}
	return nil, false, nil
}

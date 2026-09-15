//go:build integration

// Integration test for the NeuChain adapter against a LIVE 4-block-server +
// 1-epoch-server network.
//
//	bash deploy/docker/neuchain/up.sh local
//	set -a; source deploy/docker/neuchain/connection.env; set +a
//	go test -tags integration -run Integration -v ./pkg/adapters/neuchain/
//
// Self-skips if BENCH_ADAPTER_BLOCK_SERVERS is not set.
package neuchain

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

func TestIntegrationNeuChainSubmitFinality(t *testing.T) {
	if os.Getenv("BENCH_ADAPTER_BLOCK_SERVERS") == "" {
		t.Skip("BENCH_ADAPTER_BLOCK_SERVERS not set - bring up NeuChain and source connection.env")
	}
	a := &Adapter{}
	ctx := context.Background()
	err := a.Setup(ctx, adapters.AdapterConfig{Extra: map[string]any{
		"block_servers":      os.Getenv("BENCH_ADAPTER_BLOCK_SERVERS"),
		"query_endpoint":     os.Getenv("BENCH_ADAPTER_QUERY_ENDPOINT"),
		"user_priv_key_path": os.Getenv("BENCH_ADAPTER_USER_PRIV_KEY_PATH"),
		"table_name":         getenv("BENCH_ADAPTER_TABLE_NAME", "ycsb"),
		"poll_interval":      "50ms",
	}})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer a.Teardown(ctx)

	ids := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		sr, err := a.Submit(ctx, &adapters.Transaction{
			Kind: adapters.TxWrite, Key: fmt.Sprintf("itest-key-%d", i), Value: []byte("v"), Seq: uint64(i),
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if sr.TxID == "" {
			t.Fatalf("submit %d: empty tx id (signature)", i)
		}
		if sr.AckTime.Before(sr.SubmitTime) {
			t.Fatalf("submit %d: T2 before T1", i)
		}
		ids = append(ids, sr.TxID)
	}
	for i, id := range ids {
		fr, err := a.WaitForFinality(ctx, id, 60*time.Second)
		if err != nil {
			t.Fatalf("finality %d (%s): %v", i, id, err)
		}
		if !fr.Valid {
			t.Errorf("tx %d resolved non-COMMIT", i)
		}
		if fr.FinalityTime.IsZero() {
			t.Errorf("tx %d: zero finality time", i)
		}
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

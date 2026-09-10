//go:build integration

// Integration test for the Drunix adapter against a LIVE network.
//
//	bash deploy/docker/drunix/up.sh local
//	set -a; source deploy/docker/drunix/connection.env; set +a
//	go test -tags integration -run Integration -v ./pkg/adapters/drunix/
//
// Self-skips if BENCH_ADAPTER_* is not set.
package drunix

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

func envCfg(t *testing.T) map[string]any {
	t.Helper()
	keys := map[string]string{
		"BENCH_ADAPTER_ENDORSE_ENDPOINT": "endorse_endpoint",
		"BENCH_ADAPTER_COMMIT_ENDPOINT":  "commit_endpoint",
		"BENCH_ADAPTER_GATEWAY_PEER":     "gateway_peer",
		"BENCH_ADAPTER_MSP_ID":           "msp_id",
		"BENCH_ADAPTER_CERT_PATH":        "cert_path",
		"BENCH_ADAPTER_KEY_PATH":         "key_path",
		"BENCH_ADAPTER_TLS_CA_CERT_PATH": "tls_ca_cert_path",
		"BENCH_ADAPTER_CHANNEL":          "channel",
		"BENCH_ADAPTER_CHAINCODE":        "chaincode",
	}
	m := map[string]any{}
	for env, k := range keys {
		v := os.Getenv(env)
		if v == "" {
			t.Skipf("%s not set - bring up Drunix and source connection.env", env)
		}
		m[k] = v
	}
	return m
}

func TestIntegrationDrunixSubmitFinality(t *testing.T) {
	a := &Adapter{}
	ctx := context.Background()
	if err := a.Setup(ctx, adapters.AdapterConfig{Extra: envCfg(t)}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer a.Teardown(ctx)

	ids := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		sr, err := a.Submit(ctx, &adapters.Transaction{
			Kind: adapters.TxWrite, Key: "itest-key", Value: []byte("v"), Seq: uint64(i),
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
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
			t.Errorf("tx %d committed invalid", i)
		}
	}
}

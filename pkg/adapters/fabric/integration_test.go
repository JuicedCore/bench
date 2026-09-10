//go:build integration

// Integration test for the Fabric / Drunix adapter against a LIVE network.
//
//	# bring up a network first, then:
//	set -a; source deploy/docker/fabric-cft/connection.env; set +a
//	go test -tags integration -run Integration -v ./pkg/adapters/fabric/
//
// Skips itself if the BENCH_ADAPTER_* env is not set.
package fabric

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

func envConfig(t *testing.T) map[string]any {
	t.Helper()
	need := []string{
		"BENCH_ADAPTER_ENDORSE_ENDPOINT", "BENCH_ADAPTER_GATEWAY_PEER",
		"BENCH_ADAPTER_MSP_ID", "BENCH_ADAPTER_CERT_PATH", "BENCH_ADAPTER_KEY_PATH",
		"BENCH_ADAPTER_TLS_CA_CERT_PATH", "BENCH_ADAPTER_CHANNEL", "BENCH_ADAPTER_CHAINCODE",
	}
	m := map[string]any{}
	for _, k := range need {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s not set - bring up a network and source connection.env", k)
		}
		m[cfgKey(k)] = v
	}
	return m
}

func cfgKey(env string) string {
	switch env {
	case "BENCH_ADAPTER_ENDORSE_ENDPOINT":
		return "endorse_endpoint"
	case "BENCH_ADAPTER_GATEWAY_PEER":
		return "gateway_peer"
	case "BENCH_ADAPTER_MSP_ID":
		return "msp_id"
	case "BENCH_ADAPTER_CERT_PATH":
		return "cert_path"
	case "BENCH_ADAPTER_KEY_PATH":
		return "key_path"
	case "BENCH_ADAPTER_TLS_CA_CERT_PATH":
		return "tls_ca_cert_path"
	case "BENCH_ADAPTER_CHANNEL":
		return "channel"
	case "BENCH_ADAPTER_CHAINCODE":
		return "chaincode"
	}
	return env
}

func TestIntegrationSubmitFinality(t *testing.T) {
	cfg := envConfig(t)
	a := &Adapter{name: "fabric-cft"}
	ctx := context.Background()
	if err := a.Setup(ctx, adapters.AdapterConfig{Extra: cfg}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer a.Teardown(ctx)

	const n = 10
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sr, err := a.Submit(ctx, &adapters.Transaction{
			Kind: adapters.TxWrite, Key: "itest-key", Value: []byte("v"), Seq: uint64(i),
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if sr.AckTime.Before(sr.SubmitTime) {
			t.Fatalf("submit %d: T2 (%s) before T1 (%s)", i, sr.AckTime, sr.SubmitTime)
		}
		ids = append(ids, sr.TxID)
	}

	for i, id := range ids {
		fr, err := a.WaitForFinality(ctx, id, 60*time.Second)
		if err != nil {
			t.Fatalf("finality %d (%s): %v", i, id, err)
		}
		if !fr.Valid {
			t.Errorf("tx %d (%s) committed invalid", i, id)
		}
		if fr.FinalityTime.IsZero() {
			t.Errorf("tx %d (%s): zero finality time", i, id)
		}
	}
}

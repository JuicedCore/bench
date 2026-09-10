//go:build integration

// Integration test for the Fabric-X adapter against a LIVE fabric-x-samples
// tokens stack.
//
//	bash deploy/docker/fabricx/up.sh local
//	set -a; source deploy/docker/fabricx/connection.env; set +a
//	go test -tags integration -run Integration -v ./pkg/adapters/fabricx/
//
// Self-skips if BENCH_ADAPTER_OWNER_URL is not set. Exercises the token
// `transfer` path (works today); the `/kv` path is skipped while the kvview
// service is a stub.
package fabricx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

func TestIntegrationFabricXTransfer(t *testing.T) {
	owner := os.Getenv("BENCH_ADAPTER_OWNER_URL")
	if owner == "" {
		t.Skip("BENCH_ADAPTER_OWNER_URL not set - bring up fabric-x-samples tokens and source connection.env")
	}
	a := &Adapter{}
	ctx := context.Background()
	if err := a.Setup(ctx, adapters.AdapterConfig{Extra: map[string]any{
		"owner_url":         owner,
		"issuer_url":        os.Getenv("BENCH_ADAPTER_ISSUER_URL"),
		"kv_url":            "", // force issue/transfer path, skip the stub /kv
		"sender_account":    envOrDefault("BENCH_ADAPTER_SENDER_ACCOUNT", "alice"),
		"counterparty_node": envOrDefault("BENCH_ADAPTER_COUNTERPARTY_NODE", "owner2"),
		"token_code":        envOrDefault("BENCH_ADAPTER_TOKEN_CODE", "EURX"),
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer a.Teardown(ctx)

	// Seed funds so transfers have something to move.
	if iss := os.Getenv("BENCH_ADAPTER_ISSUER_URL"); iss != "" {
		sr, err := a.Submit(ctx, &adapters.Transaction{Kind: adapters.TxWrite, Key: "alice", Seq: 0})
		if err == nil {
			_, _ = a.WaitForFinality(ctx, sr.TxID, 60*time.Second)
		}
	}

	for i := 0; i < 5; i++ {
		sr, err := a.Submit(ctx, &adapters.Transaction{
			Kind: adapters.TxTransfer, Key: "alice", DestKey: "bob", Amount: 1, Seq: uint64(i + 1),
		})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		fr, err := a.WaitForFinality(ctx, sr.TxID, 90*time.Second)
		if err != nil {
			t.Fatalf("finality %d: %v", i, err)
		}
		if !fr.Valid || fr.TxID == "" {
			t.Errorf("transfer %d: %+v", i, fr)
		}
		// Fabric-X: submit latency is N/A (POST blocks to finality) - only
		// assert the E2E ordering holds.
		if fr.FinalityTime.Before(sr.SubmitTime) {
			t.Errorf("transfer %d: T3 before T1", i)
		}
	}
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

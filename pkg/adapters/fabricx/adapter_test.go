package fabricx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

// fakeFabricX implements the real fabric-x-samples token routes + the custom
// /kv route. Token/kv POSTs are synchronous to finality (respond after delay).
type fakeFabricX struct {
	seq   atomic.Int64
	delay time.Duration
}

func (f *fakeFabricX) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"message": "ok"})
	})
	mux.HandleFunc("/kv", func(w http.ResponseWriter, r *http.Request) {
		var req kvRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		time.Sleep(f.delay)
		id := "kvtx-" + itoa(f.seq.Add(1))
		if req.Op == "read" {
			writeJSON(w, kvResponse{TxID: id, Value: "", Found: false})
			return
		}
		writeJSON(w, kvResponse{TxID: id})
	})
	mux.HandleFunc("/owner/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			time.Sleep(f.delay)
			writeJSON(w, tokenResponse{Message: "transferred tokens", Payload: "tok-" + itoa(f.seq.Add(1))})
			return
		}
		writeJSON(w, accountResponse{Message: "ok", Payload: account{
			ID: "alice", Balance: []amount{{Code: "EURX", Value: 10000}},
		}})
	})
	mux.HandleFunc("/issuer/issue", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(f.delay)
		writeJSON(w, tokenResponse{Message: "issued", Payload: "iss-" + itoa(f.seq.Add(1))})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func setup(t *testing.T, srvURL string) *Adapter {
	t.Helper()
	a := &Adapter{}
	err := a.Setup(context.Background(), adapters.AdapterConfig{Extra: map[string]any{
		"owner_url":  srvURL,
		"issuer_url": srvURL,
		"kv_url":     srvURL,
	}})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return a
}

func TestFabricXKVWriteFinality(t *testing.T) {
	fake := &fakeFabricX{delay: 120 * time.Millisecond}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	a := setup(t, srv.URL)

	ctx := context.Background()
	sr, err := a.Submit(ctx, &adapters.Transaction{Kind: adapters.TxWrite, Key: "k1", Value: []byte("v1"), Seq: 1})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if sr.TxID == "" || !strings.HasPrefix(sr.TxID, "fx-") {
		t.Fatalf("expected local correlation id, got %q", sr.TxID)
	}
	// Submit must return promptly (before the synchronous POST finishes).
	if time.Since(sr.SubmitTime) > 80*time.Millisecond {
		t.Errorf("Submit blocked %s - should return immediately", time.Since(sr.SubmitTime))
	}

	fr, err := a.WaitForFinality(ctx, sr.TxID, 5*time.Second)
	if err != nil {
		t.Fatalf("finality: %v", err)
	}
	if !fr.Valid || !strings.HasPrefix(fr.TxID, "kvtx-") {
		t.Errorf("expected committed real kv tx id, got %+v", fr)
	}
	if fr.FinalityTime.Sub(sr.SubmitTime) < 120*time.Millisecond {
		t.Errorf("finality returned before the synchronous POST could complete: %s", fr.FinalityTime.Sub(sr.SubmitTime))
	}
}

func TestFabricXTransfer(t *testing.T) {
	fake := &fakeFabricX{delay: 10 * time.Millisecond}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	a := setup(t, srv.URL)

	sr, err := a.Submit(context.Background(), &adapters.Transaction{
		Kind: adapters.TxTransfer, Key: "alice", DestKey: "bob", Amount: 1, Seq: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := a.WaitForFinality(context.Background(), sr.TxID, 3*time.Second)
	if err != nil || !fr.Valid || !strings.HasPrefix(fr.TxID, "tok-") {
		t.Fatalf("transfer finality: fr=%+v err=%v", fr, err)
	}
}

func TestFabricXReadReturnsQuickly(t *testing.T) {
	fake := &fakeFabricX{delay: 5 * time.Millisecond}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	a := setup(t, srv.URL)

	sr, _ := a.Submit(context.Background(), &adapters.Transaction{Kind: adapters.TxRead, Key: "k1", Seq: 3})
	fr, err := a.WaitForFinality(context.Background(), sr.TxID, time.Second)
	if err != nil || !fr.Valid {
		t.Fatalf("read finality: fr=%+v err=%v", fr, err)
	}
}

func TestFabricXUnreachable(t *testing.T) {
	a := &Adapter{}
	err := a.Setup(context.Background(), adapters.AdapterConfig{Extra: map[string]any{
		"owner_url": "http://127.0.0.1:1",
		"kv_url":    "http://127.0.0.1:1",
	}})
	if err == nil {
		t.Fatal("expected unreachable error")
	}
}

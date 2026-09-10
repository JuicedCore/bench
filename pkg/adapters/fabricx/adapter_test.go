package fabricx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

// fakeFabricX implements the assumed REST contract: POST accepts a tx and marks
// it pending; it commits after commitAfter; GET returns its status.
type fakeFabricX struct {
	mu          sync.Mutex
	seq         int
	acceptedAt  map[string]time.Time
	commitAfter time.Duration
}

func (f *fakeFabricX) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/kv", f.submit)
	mux.HandleFunc("/api/v1/tokens/transfer", f.submit)
	mux.HandleFunc("/api/v1/tx/", f.status)
	return mux
}

func (f *fakeFabricX) submit(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seq++
	id := "fx-" + itoa(f.seq)
	f.acceptedAt[id] = time.Now()
	f.mu.Unlock()
	writeJSON(w, map[string]any{"txID": id, "accepted": true})
}

func (f *fakeFabricX) status(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tx/")
	id = strings.TrimSuffix(id, "/wait")
	f.mu.Lock()
	at, ok := f.acceptedAt[id]
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	st := "pending"
	var bn uint64
	if time.Since(at) >= f.commitAfter {
		st, bn = "committed", 42
	}
	writeJSON(w, map[string]any{"txID": id, "status": st, "blockNum": bn})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
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

func TestFabricXSubmitAndFinality(t *testing.T) {
	fake := &fakeFabricX{acceptedAt: map[string]time.Time{}, commitAfter: 150 * time.Millisecond}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	a := &Adapter{}
	err := a.Setup(context.Background(), adapters.AdapterConfig{Extra: map[string]any{
		"base_url":      srv.URL,
		"finality_mode": "poll",
	}})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	ctx := context.Background()
	sr, err := a.Submit(ctx, &adapters.Transaction{Kind: adapters.TxWrite, Key: "k1", Value: []byte("v1"), Seq: 1})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if sr.TxID == "" || sr.AckTime.Before(sr.SubmitTime) {
		t.Fatalf("bad submit result: %+v", sr)
	}

	fr, err := a.WaitForFinality(ctx, sr.TxID, 5*time.Second)
	if err != nil {
		t.Fatalf("finality: %v", err)
	}
	if !fr.Valid || fr.BlockNum != 42 {
		t.Errorf("expected committed in block 42, got %+v", fr)
	}
	if fr.FinalityTime.Sub(sr.SubmitTime) < 150*time.Millisecond {
		t.Errorf("finality returned too early: %s", fr.FinalityTime.Sub(sr.SubmitTime))
	}
}

func TestFabricXReadFinalizesImmediately(t *testing.T) {
	fake := &fakeFabricX{acceptedAt: map[string]time.Time{}, commitAfter: time.Hour}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	a := &Adapter{}
	if err := a.Setup(context.Background(), adapters.AdapterConfig{Extra: map[string]any{"base_url": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	sr, err := a.Submit(context.Background(), &adapters.Transaction{Kind: adapters.TxRead, Key: "k1", Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := a.WaitForFinality(context.Background(), sr.TxID, time.Second)
	if err != nil || !fr.Valid {
		t.Fatalf("read should finalize immediately: fr=%+v err=%v", fr, err)
	}
}

func TestFabricXUnreachable(t *testing.T) {
	a := &Adapter{}
	err := a.Setup(context.Background(), adapters.AdapterConfig{Extra: map[string]any{
		"base_url": "http://127.0.0.1:1", // nothing listening
	}})
	if err == nil {
		t.Fatal("expected unreachable error")
	}
}

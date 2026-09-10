// kvview is the custom Fabric-X REST service that gives the benchmark harness a
// normalized key/value path Fabric-X's token-only sample API otherwise lacks
// (docs/decisions/adr-003-fabricx-fsc-view-and-rest.md).
//
// STATUS: Phase 3 stub. This build serves /healthz and /kv, but /kv returns
// 501 until the FSC KV views are wired (see README.md for the implementation
// spec). The container runs so the topology and the adapter health check work.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

type kvRequest struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value,omitempty"` // base64
}

type kvResponse struct {
	TxID  string `json:"txID,omitempty"`
	Value string `json:"value,omitempty"`
	Found bool   `json:"found,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	port := flag.String("port", envOr("KVVIEW_PORT", "9700"), "listen port")
	_ = flag.String("conf", envOr("KVVIEW_CONF", "/etc/fabricx/kvview"), "FSC core.yaml directory (used by the real impl)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
	})
	mux.HandleFunc("/kv", handleKV)

	srv := &http.Server{Addr: net.JoinHostPort("0.0.0.0", *port), Handler: mux}
	go func() {
		log.Printf("kvview listening on :%s", *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = srv.Close()
}

func handleKV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, kvResponse{Error: "POST only"})
		return
	}
	var req kvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, kvResponse{Error: err.Error()})
		return
	}
	// TODO(phase3): dispatch to the FSC KV views (see README.md).
	//   write -> service.WriteView{Key, Value}  -> ordering + finality -> txID
	//   read  -> service.ReadView{Key}          -> value, found
	writeJSON(w, http.StatusNotImplemented, kvResponse{
		Error: "kvview FSC views not wired yet (Phase 3) - op=" + req.Op + " key=" + req.Key,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

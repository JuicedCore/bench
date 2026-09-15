package fabric

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigErrorsNameKeysAndHint(t *testing.T) {
	_, err := configFromExtra("fabric-cft", map[string]any{"peer_endpoint": ""})
	if err == nil {
		t.Fatal("expected error for empty config")
	}
	for _, want := range []string{"adapter.peer_endpoint", "adapter.cert_path", "adapter.tls_ca_cert_path", "connection.env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestConfigReadsDeclaredKeysAndChecksKeyPath(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	_ = os.WriteFile(cert, []byte("x"), 0o644)
	extra := map[string]any{
		"peer_endpoint": "localhost:7051", "cert_path": cert, "tls_ca_cert_path": cert,
		"key_path": filepath.Join(dir, "missing-key"), "endorse_timeout": "3s", "fn_put": "Set",
	}
	if _, err := configFromExtra("fabric-cft", extra); err == nil || !strings.Contains(err.Error(), "adapter.key_path") {
		t.Errorf("missing key_path file must be reported, got %v", err)
	}
	extra["key_path"] = cert
	c, err := configFromExtra("fabric-cft", extra)
	if err != nil {
		t.Fatal(err)
	}
	if c.EndorseTimeout != 3*time.Second || c.FnPut != "Set" {
		t.Errorf("declared keys ignored: endorse_timeout=%s fn_put=%q", c.EndorseTimeout, c.FnPut)
	}
	extra["submit_timeout"] = "soon"
	if _, err := configFromExtra("fabric-cft", extra); err == nil || !strings.Contains(err.Error(), "submit_timeout") {
		t.Errorf("bad duration must be reported, got %v", err)
	}
}

func TestClassifyProbeError(t *testing.T) {
	cases := []struct {
		err   error
		fatal bool
		hint  string
	}{
		{status.Error(codes.Unavailable, "connection refused"), true, "unreachable"},
		{status.Error(codes.Unavailable, "x509: certificate is valid for peer0.org2"), true, "gateway_peer"},
		{status.Error(codes.PermissionDenied, "access denied"), true, "identity"},
		{errors.New("chaincode definition for 'kvstore' not found"), true, "deployed"},
		{errors.New("some chaincode-level failure"), false, ""},
	}
	for _, c := range cases {
		fatal, hint := classifyProbeError(c.err)
		if fatal != c.fatal || !strings.Contains(hint, c.hint) {
			t.Errorf("%v: fatal=%v hint=%q, want fatal=%v hint~%q", c.err, fatal, hint, c.fatal, c.hint)
		}
	}
}

func TestTeardownIdempotentWithoutSetup(t *testing.T) {
	a := &Adapter{name: "fabric-cft"}
	if err := a.Teardown(nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Teardown(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(nil, nil); err == nil {
		t.Error("Submit before Setup must error")
	}
}

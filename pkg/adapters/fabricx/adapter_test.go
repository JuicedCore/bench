package fabricx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"

	"github.com/juicedcore/bench/pkg/adapters"
)

// writeTestKey emits a PKCS#8 PEM ECDSA key, the format up.sh exports and the
// committer parses.
func writeTestKey(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ns.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestLoadNsSignerRejectsNonPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNsSigner(path); err == nil {
		t.Fatal("expected an error for a non-PEM key file")
	}
}

// The endorsement must verify under exactly the rule the committer applies:
// ecdsa.VerifyASN1 over sha256 of the namespace's ASN.1 marshalling. If either
// half drifts, every transaction fails validation on a live network with no
// local signal, so this pins it.
func TestEndorsementVerifiesTheWayTheCommitterChecksIt(t *testing.T) {
	path, key := writeTestKey(t)
	signer, err := loadNsSigner(path)
	if err != nil {
		t.Fatal(err)
	}

	const txID = "tx-under-test"
	tx := &applicationpb.Tx{Namespaces: []*applicationpb.TxNamespace{{
		NsId:        "0",
		NsVersion:   0,
		BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}},
	}}}

	if err := signer.endorse(txID, tx); err != nil {
		t.Fatal(err)
	}
	if len(tx.Endorsements) != 1 {
		t.Fatalf("got %d endorsement sets, want 1 per namespace", len(tx.Endorsements))
	}
	sigs := tx.Endorsements[0].GetEndorsementsWithIdentity()
	if len(sigs) != 1 {
		t.Fatalf("got %d signatures, want 1", len(sigs))
	}

	msg, err := tx.Namespaces[0].ASN1Marshal(txID, tx.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], sigs[0].GetEndorsement()) {
		t.Error("endorsement does not verify under sha256 + ecdsa.VerifyASN1")
	}

	// A different txID must not verify: the ID is inside the signed payload, so
	// signatures cannot be replayed onto another transaction.
	other, err := tx.Namespaces[0].ASN1Marshal("some-other-tx", tx.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := sha256.Sum256(other)
	if ecdsa.VerifyASN1(&key.PublicKey, otherDigest[:], sigs[0].GetEndorsement()) {
		t.Error("endorsement verified against a different txID; the ID is not being bound in")
	}
}

func TestNamespaceForReadIsRejected(t *testing.T) {
	a := &Adapter{cfg: &Config{Namespace: "0", ChannelID: "arma"}}
	_, err := a.namespaceFor(&adapters.Transaction{Kind: adapters.TxRead, Key: "key-1", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "QueryService") {
		t.Fatalf("reads must not become an envelope, got %v", err)
	}
}

func TestWriteAndTransferShape(t *testing.T) {
	a := &Adapter{cfg: &Config{Namespace: "0", ChannelID: "arma"}}

	w, err := a.namespaceFor(&adapters.Transaction{Kind: adapters.TxWrite, Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.BlindWrites) != 1 || len(w.ReadWrites) != 0 {
		t.Errorf("kv-write should be a single blind write, got %d blind / %d rw", len(w.BlindWrites), len(w.ReadWrites))
	}

	// transfer touches two accounts, which is what makes MVCC conflicts visible
	// and must match the two-key shape the other platforms produce.
	tr, err := a.namespaceFor(&adapters.Transaction{
		Kind: adapters.TxTransfer, Key: "acct-1", DestKey: "acct-2", Value: []byte("1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.ReadWrites) != 2 {
		t.Fatalf("transfer should read-modify-write 2 accounts, got %d", len(tr.ReadWrites))
	}
}

func TestConfigRejectsMissingEndpoints(t *testing.T) {
	// Unset ${VAR} arrives as "" - it must be treated as missing, not accepted.
	if _, err := configFromExtra(map[string]any{
		"broadcast_endpoint": "", "deliver_endpoint": "", "signing_key_path": "",
	}); err == nil {
		t.Error("expected an error when the endpoints are empty")
	}
}

func TestConfigIgnoresForeignKeys(t *testing.T) {
	// The shared normalized config carries every platform's keys; fabricx must
	// ignore the others rather than choking on them.
	cfg, err := configFromExtra(map[string]any{
		"broadcast_endpoint": "localhost:6022",
		"deliver_endpoint":   "localhost:4001",
		"signing_key_path":   "/tmp/k.pem",
		"peer_endpoint":      "localhost:7051",
		"block_servers":      "localhost:5001",
		"poll_interval":      "50ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChannelID != "arma" || cfg.Namespace != "0" {
		t.Errorf("defaults not applied: channel=%q ns=%q", cfg.ChannelID, cfg.Namespace)
	}
}

func TestConfigReadsQueryEndpoint(t *testing.T) {
	cfg, err := configFromExtra(map[string]any{
		"broadcast_endpoint": "localhost:6022",
		"deliver_endpoint":   "localhost:4001",
		"signing_key_path":   "/tmp/k.pem",
		"query_endpoint":     "localhost:7001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueryEndpoint != "localhost:7001" {
		t.Errorf("QueryEndpoint = %q, want localhost:7001", cfg.QueryEndpoint)
	}
}

func TestConfigSplitsBroadcastEndpoints(t *testing.T) {
	cfg, err := configFromExtra(map[string]any{
		"broadcast_endpoint": "localhost:6022, localhost:6122,,localhost:6222",
		"deliver_endpoint":   "localhost:4001",
		"signing_key_path":   "/tmp/k.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"localhost:6022", "localhost:6122", "localhost:6222"}
	if strings.Join(cfg.BroadcastEndpoints, "|") != strings.Join(want, "|") {
		t.Errorf("BroadcastEndpoints = %q, want %q", cfg.BroadcastEndpoints, want)
	}
}

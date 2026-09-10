package neuchain

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/juicedcore/bench/pkg/adapters"
	pb "github.com/juicedcore/bench/pkg/adapters/neuchain/proto"
)

func writeTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	p := filepath.Join(t.TempDir(), "user.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSignerRoundTrip(t *testing.T) {
	s, err := loadSigner(writeTestKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("neuchain transaction payload bytes")
	sig, err := s.sign(msg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(msg)
	if err := rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	// RSA-1024 signature is 128 bytes.
	if len(sig) != 128 {
		t.Errorf("sig len = %d, want 128", len(sig))
	}
}

func TestLoadSignerRejectsEncrypted(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	der := x509.MarshalPKCS1PrivateKey(key)
	//nolint:staticcheck
	blk, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", der, []byte("pw"), x509.PEMCipherAES256)
	if err != nil {
		t.Skip("EncryptPEMBlock unavailable")
	}
	p := filepath.Join(t.TempDir(), "enc.pem")
	os.WriteFile(p, pem.EncodeToMemory(blk), 0o600)
	if _, err := loadSigner(p, "pw"); err == nil {
		t.Fatal("expected encrypted key to be rejected")
	}
}

func TestDecodeResultFrame(t *testing.T) {
	// build a frame: tid=7, epoch=42, digest="abc", result=COMMIT
	var buf []byte
	var tmp [binary.MaxVarintLen64]byte
	put := func(v uint64) { n := binary.PutUvarint(tmp[:], v); buf = append(buf, tmp[:n]...) }
	put(7)
	put(42)
	digest := []byte("abc")
	put(uint64(len(digest)))
	buf = append(buf, digest...)
	put(uint64(resCommit))

	f, err := decodeResultFrame(buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.TID != 7 || f.Epoch != 42 || string(f.Digest) != "abc" || !f.valid() {
		t.Fatalf("bad decode: %+v", f)
	}

	// abort -> invalid
	buf = buf[:0]
	put(1)
	put(2)
	put(1)
	buf = append(buf, 'x')
	put(uint64(resAbort))
	f, err = decodeResultFrame(buf)
	if err != nil {
		t.Fatal(err)
	}
	if f.valid() {
		t.Error("ABORT frame should be invalid")
	}
}

func TestDecodeResultFrameTruncated(t *testing.T) {
	if _, err := decodeResultFrame([]byte{0x80}); err == nil {
		t.Fatal("expected error on truncated varint")
	}
}

func TestBuildInvokeShape(t *testing.T) {
	a := &Adapter{}
	var err error
	a.cfg, err = configFromExtra(map[string]any{
		"block_servers":      "127.0.0.1:5001",
		"user_priv_key_path": writeTestKey(t),
		"func_name":          "ycsb",
		"table_name":         "test_table",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.signer, err = loadSigner(a.cfg.UserPrivKeyPath, ""); err != nil {
		t.Fatal(err)
	}

	wire, sig, err := a.buildInvoke(&adapters.Transaction{
		Kind: adapters.TxTransfer, Key: "acct-1", DestKey: "acct-2", Amount: 1, Seq: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var req pb.UserRequest
	if err := proto.Unmarshal(wire, &req); err != nil {
		t.Fatal(err)
	}
	if string(req.Digest) != string(sig) {
		t.Error("UserRequest.digest must equal the returned signature")
	}
	var payload pb.TransactionPayload
	if err := proto.Unmarshal(req.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload.Header) != "ycsb" {
		t.Errorf("header = %q, want ycsb", payload.Header)
	}
	if payload.Nonce&0xffffffff != 5 {
		t.Errorf("nonce low 32 bits = %d, want seq 5", payload.Nonce&0xffffffff)
	}
	var yp pb.YCSB_PAYLOAD
	if err := proto.Unmarshal(payload.Payload, &yp); err != nil {
		t.Fatal(err)
	}
	if len(yp.Reads) != 2 || len(yp.Update) != 2 || string(yp.Table) != "test_table" {
		t.Errorf("transfer should map to 2 reads + 2 updates on test_table: %+v", &yp)
	}
	// signature must be over the marshaled TransactionPayload
	sum := sha256.Sum256(req.Payload)
	if err := rsa.VerifyPKCS1v15(&a.signer.key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature not over TransactionPayload bytes: %v", err)
	}
}

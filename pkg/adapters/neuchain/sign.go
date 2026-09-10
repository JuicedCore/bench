package neuchain

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// signer produces the RSA-1024 PKCS#1 v1.5 signature over SHA-256(message) that
// NeuChain's OpenSSL EVP_SignFinal(EVP_sha256()) path expects. The signature
// bytes double as the transaction id (comm.UserRequest.digest).
type signer struct {
	key *rsa.PrivateKey
}

// loadSigner reads a PEM PKCS#1 RSA private key ("RSA PRIVATE KEY"). NeuChain
// generates these with an empty password; an encrypted key is rejected with a
// clear error (decrypt it out of band, or pass the passphrase-less copy).
func loadSigner(path, _password string) (*signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("neuchain: read user key: %w", err)
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, fmt.Errorf("neuchain: %s: not PEM", path)
	}
	//nolint:staticcheck // NeuChain keys use the legacy encrypted-PEM header
	if x509.IsEncryptedPEMBlock(blk) {
		return nil, fmt.Errorf("neuchain: %s is an encrypted PEM key; NeuChain's own keys are unencrypted - re-init crypto or supply a decrypted copy", path)
	}
	key, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
	if err != nil {
		// some builds emit PKCS#8
		if k8, e2 := x509.ParsePKCS8PrivateKey(blk.Bytes); e2 == nil {
			if rk, ok := k8.(*rsa.PrivateKey); ok {
				return &signer{key: rk}, nil
			}
		}
		return nil, fmt.Errorf("neuchain: parse PKCS#1 RSA key: %w", err)
	}
	return &signer{key: key}, nil
}

// sign returns the detached signature over msg.
func (s *signer) sign(msg []byte) ([]byte, error) {
	sum := sha256.Sum256(msg)
	return rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
}

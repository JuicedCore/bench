package neuchain

import (
	"crypto"
	"crypto/cipher"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// signer produces the RSA-1024 PKCS#1 v1.5 signature over SHA-256(message) that
// NeuChain's OpenSSL EVP_SignFinal(EVP_sha256()) path expects. The signature
// bytes double as the transaction id (comm.UserRequest.digest).
type signer struct {
	key *rsa.PrivateKey
}

// loadSigner reads a PEM PKCS#1 RSA private key ("RSA PRIVATE KEY").
// NeuChain's CryptoSign::generateKeyFiles writes it with OpenSSL's legacy PEM
// encryption (DES-EDE3-OFB) under an empty password, so an encrypted key is
// decrypted with password (normally ""), not rejected.
func loadSigner(path, password string) (*signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("neuchain: read user key: %w", err)
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, fmt.Errorf("neuchain: %s: not PEM", path)
	}
	der := blk.Bytes
	if blk.Headers["Proc-Type"] == "4,ENCRYPTED" {
		if der, err = decryptLegacyPEM(blk, []byte(password)); err != nil {
			return nil, fmt.Errorf("neuchain: %s: %w", path, err)
		}
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		// some builds emit PKCS#8
		if k8, e2 := x509.ParsePKCS8PrivateKey(der); e2 == nil {
			if rk, ok := k8.(*rsa.PrivateKey); ok {
				return &signer{key: rk}, nil
			}
		}
		return nil, fmt.Errorf("neuchain: parse PKCS#1 RSA key (wrong key_password?): %w", err)
	}
	return &signer{key: key}, nil
}

// decryptLegacyPEM undoes OpenSSL's PEM_write_RSAPrivateKey encryption. Go's
// x509.DecryptPEMBlock covers only the CBC ciphers; NeuChain uses DES-EDE3-OFB,
// a stream mode, so that one is done here: key = EVP_BytesToKey(MD5, salt =
// IV[:8], one iteration), no padding.
func decryptLegacyPEM(blk *pem.Block, password []byte) ([]byte, error) {
	alg, ivHex, ok := strings.Cut(blk.Headers["DEK-Info"], ",")
	if !ok {
		return nil, fmt.Errorf("encrypted PEM without a DEK-Info header")
	}
	if alg != "DES-EDE3-OFB" {
		//nolint:staticcheck // legacy PEM encryption is what the key uses
		der, err := x509.DecryptPEMBlock(blk, password)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s PEM key: %w", alg, err)
		}
		return der, nil
	}
	iv, err := hex.DecodeString(ivHex)
	if err != nil || len(iv) != des.BlockSize {
		return nil, fmt.Errorf("bad DEK-Info IV %q", ivHex)
	}
	c, err := des.NewTripleDESCipher(evpBytesToKey(password, iv[:8], 24))
	if err != nil {
		return nil, err
	}
	der := make([]byte, len(blk.Bytes))
	//nolint:staticcheck // OFB is the mode NeuChain's keys are written in
	cipher.NewOFB(c, iv).XORKeyStream(der, blk.Bytes)
	return der, nil
}

// evpBytesToKey is OpenSSL's EVP_BytesToKey with MD5 and one iteration.
func evpBytesToKey(password, salt []byte, n int) []byte {
	var key, prev []byte
	for len(key) < n {
		h := md5.New()
		h.Write(prev)
		h.Write(password)
		h.Write(salt)
		prev = h.Sum(nil)
		key = append(key, prev...)
	}
	return key[:n]
}

// sign returns the detached signature over msg.
func (s *signer) sign(msg []byte) ([]byte, error) {
	sum := sha256.Sum256(msg)
	return rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
}

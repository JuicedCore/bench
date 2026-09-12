package fabricx

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
)

// nsSigner endorses a transaction's namespace with the key registered as that
// namespace's policy.
//
// The scheme is fixed by what the committer verifies in
// utils/signature/verify_ecdsa.go: the digest is sha256 over the namespace's
// ASN.1 marshalling (which upstream's own applicationpb produces, so we cannot
// drift from it), and the signature is ASN.1 DER - i.e. exactly stdlib
// ecdsa.SignASN1 / VerifyASN1. Getting either wrong makes every transaction
// fail signature validation rather than erroring visibly, so both come from
// upstream's definitions rather than being re-derived here.
type nsSigner struct {
	key *ecdsa.PrivateKey
}

// loadNsSigner reads a PKCS#8 PEM ECDSA private key. up.sh generates this key,
// registers its public half as the namespace policy, and writes the path into
// connection.env.
func loadNsSigner(path string) (*nsSigner, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fabricx: read signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("fabricx: %s is not PEM", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("fabricx: parse PKCS#8 key %s: %w", path, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("fabricx: %s is not an ECDSA key (%T)", path, parsed)
	}
	return &nsSigner{key: key}, nil
}

// endorse signs every namespace of tx in place.
func (s *nsSigner) endorse(txID string, tx *applicationpb.Tx) error {
	tx.Endorsements = make([]*applicationpb.Endorsements, len(tx.Namespaces))
	for i, ns := range tx.Namespaces {
		msg, err := ns.ASN1Marshal(txID, tx.Metadata)
		if err != nil {
			return fmt.Errorf("fabricx: marshal namespace %d: %w", i, err)
		}
		digest := sha256.Sum256(msg)
		sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
		if err != nil {
			return fmt.Errorf("fabricx: sign namespace %d: %w", i, err)
		}
		tx.Endorsements[i] = &applicationpb.Endorsements{
			EndorsementsWithIdentity: []*applicationpb.EndorsementWithIdentity{{Endorsement: sig}},
		}
	}
	return nil
}

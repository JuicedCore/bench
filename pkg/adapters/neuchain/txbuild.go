package neuchain

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/juicedcore/bench/pkg/adapters"
	pb "github.com/juicedcore/bench/pkg/adapters/neuchain/proto"
)

// buildInvoke mirrors NeuChain's DBUserBase::sendInvokeRequest +
// Utils::getTransactionPayload + Utils::getUserInvokeRequest:
//
//	YCSB_PAYLOAD{ table, reads, update }         (from the normalized tx)
//	TransactionPayload{ header=funcName, payload=marshal(YCSB_PAYLOAD),
//	                    nonce = rand64 | (seq<<32), digest="" }
//	sig = RSA_PKCS1v15_SHA256(userPrivKey, marshal(TransactionPayload))
//	UserRequest{ payload=marshal(TransactionPayload), digest=sig }
//
// Returns the serialized UserRequest to publish and the signature (= tx id).
func (a *Adapter) buildInvoke(tx *adapters.Transaction) (wire []byte, sig []byte, err error) {
	reads, writes := mapKeys(tx)

	yp := &pb.YCSB_PAYLOAD{Table: []byte(a.cfg.TableName)}
	for _, r := range reads {
		yp.Reads = append(yp.Reads, []byte(r))
	}
	for _, w := range writes {
		yp.Update = append(yp.Update, []byte(w))
	}
	ypRaw, err := proto.Marshal(yp)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal YCSB_PAYLOAD: %w", err)
	}

	payload := &pb.TransactionPayload{
		Header:  []byte(a.cfg.FuncName),
		Payload: ypRaw,
		Nonce:   nonce(tx.Seq),
	}
	payloadRaw, err := proto.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal TransactionPayload: %w", err)
	}

	sig, err = a.signer.sign(payloadRaw)
	if err != nil {
		return nil, nil, err
	}

	req := &pb.UserRequest{Payload: payloadRaw, Digest: sig}
	wire, err = proto.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal UserRequest: %w", err)
	}
	return wire, sig, nil
}

// mapKeys turns a normalized transaction into NeuChain read / update key lists.
// Matches docs/platforms/neuchain-client-implementation.md §3.3.
func mapKeys(tx *adapters.Transaction) (reads, writes []string) {
	switch tx.Kind {
	case adapters.TxRead:
		return []string{tx.Key}, nil
	case adapters.TxTransfer:
		return []string{tx.Key, tx.DestKey}, []string{tx.Key, tx.DestKey}
	default: // TxWrite
		return nil, []string{tx.Key}
	}
}

// nonce = rand64 with the low 32 bits replaced by seq, matching
// `random() + (seed << 32)` closely enough for replay-uniqueness.
func nonce(seq uint64) uint64 {
	var b [8]byte
	_, _ = cryptorand.Read(b[:])
	hi := binary.LittleEndian.Uint64(b[:]) &^ uint64(0xffffffff)
	return hi | (seq & 0xffffffff)
}

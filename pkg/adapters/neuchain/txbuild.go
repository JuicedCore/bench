package neuchain

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/juicedcore/bench/pkg/adapters"
	pb "github.com/juicedcore/bench/pkg/adapters/neuchain/proto"
)

// ycsbField is the single field every normalized write sets on a YCSB record.
const ycsbField = "field0"

// buildInvoke mirrors NeuChain's AriaYCSB_DB::Update / ::Read +
// DBUserBase::sendInvokeRequest + Utils::getTransactionPayload:
//
//	YCSB_PAYLOAD{ table, reads = [key, marshal(YCSB_FOR_BLOCK_BENCH)] }
//	TransactionPayload{ header = "write" | "read", payload = marshal(YCSB_PAYLOAD),
//	                    nonce = rand64 | (seq<<32), digest="" }
//	sig = RSA_PKCS1v15_SHA256(userPrivKey, marshal(TransactionPayload))
//	UserRequest{ payload=marshal(TransactionPayload), digest=sig }
//
// The header selects the YCSB_Chaincode function; any other header makes
// InvokeChaincode return 0 and the server aborts the tx (ABORT_NO_RETRY).
// Returns the serialized UserRequest to publish and the signature (= tx id).
func (a *Adapter) buildInvoke(tx *adapters.Transaction) (wire []byte, sig []byte, err error) {
	funcName, record, err := ycsbCall(tx)
	if err != nil {
		return nil, nil, err
	}
	recordRaw, err := proto.Marshal(record)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal YCSB_FOR_BLOCK_BENCH: %w", err)
	}
	ypRaw, err := proto.Marshal(&pb.YCSB_PAYLOAD{
		Table: []byte(a.cfg.TableName),
		Reads: [][]byte{[]byte(tx.Key), recordRaw},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal YCSB_PAYLOAD: %w", err)
	}

	payload := &pb.TransactionPayload{
		Header:  []byte(funcName),
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

// buildHeartbeat mirrors Utils::getEmptyPayloadRaw: header "empty", an empty
// YCSB_PAYLOAD on test_table.
func (a *Adapter) buildHeartbeat(seq uint64) ([]byte, error) {
	ypRaw, err := proto.Marshal(&pb.YCSB_PAYLOAD{Table: []byte("test_table")})
	if err != nil {
		return nil, err
	}
	payloadRaw, err := proto.Marshal(&pb.TransactionPayload{Header: []byte("empty"), Payload: ypRaw, Nonce: nonce(seq)})
	if err != nil {
		return nil, err
	}
	sig, err := a.signer.sign(payloadRaw)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(&pb.UserRequest{Payload: payloadRaw, Digest: sig})
}

// ycsbCall maps a normalized transaction onto a YCSB_Chaincode function and the
// record argument it takes. A write merges field0=Value into the key's record;
// a read passes an empty field filter (all fields). The YCSB chaincode touches
// one key per call, so a two-account transfer has no faithful mapping.
func ycsbCall(tx *adapters.Transaction) (string, *pb.YCSB_FOR_BLOCK_BENCH, error) {
	switch tx.Kind {
	case adapters.TxRead:
		return "read", &pb.YCSB_FOR_BLOCK_BENCH{}, nil
	case adapters.TxWrite:
		return "write", &pb.YCSB_FOR_BLOCK_BENCH{Values: []*pb.YCSB_FOR_BLOCK_BENCH_YCSB_FOR_BLOCK_BENCH_PAIR{
			{Key: []byte(ycsbField), Value: tx.Value},
		}}, nil
	default:
		return "", nil, fmt.Errorf("transaction kind %v is not supported by the NeuChain YCSB chaincode (one key per call; transfers need the small_bank chaincode)", tx.Kind)
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

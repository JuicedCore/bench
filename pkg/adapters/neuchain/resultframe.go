package neuchain

import (
	"encoding/binary"
	"fmt"
)

// txResult mirrors NeuChain's enum TransactionResult.
type txResult uint32

const (
	resCommit       txResult = 0
	resPending      txResult = 1
	resAbort        txResult = 2
	resAbortNoRetry txResult = 3
)

// resultFrame is one decoded entry from Block.data.data, produced by
// MockTransaction::serializeResultToString:
//
//	uvarint tid
//	uvarint epoch
//	uvarint digestLen
//	bytes   digest        (== the UserRequest.digest / signature we submitted)
//	uvarint result
type resultFrame struct {
	TID    uint64
	Epoch  uint64
	Digest []byte
	Result txResult
}

// valid reports whether the transaction committed successfully. COMMIT -> true;
// ABORT / ABORT_NO_RETRY -> false (counts as a failure). PENDING should not
// appear in a committed block.
func (f resultFrame) valid() bool { return f.Result == resCommit }

// decodeResultFrame parses one hand-rolled varint frame.
func decodeResultFrame(b []byte) (resultFrame, error) {
	var f resultFrame
	off := 0

	read := func(name string) (uint64, error) {
		v, n := binary.Uvarint(b[off:])
		if n <= 0 {
			return 0, fmt.Errorf("neuchain result frame: bad varint for %s at offset %d", name, off)
		}
		off += n
		return v, nil
	}

	var err error
	if f.TID, err = read("tid"); err != nil {
		return f, err
	}
	if f.Epoch, err = read("epoch"); err != nil {
		return f, err
	}
	dl, err := read("digestLen")
	if err != nil {
		return f, err
	}
	if off+int(dl) > len(b) {
		return f, fmt.Errorf("neuchain result frame: digest length %d exceeds buffer", dl)
	}
	f.Digest = append([]byte(nil), b[off:off+int(dl)]...)
	off += int(dl)
	res, err := read("result")
	if err != nil {
		return f, err
	}
	f.Result = txResult(res)
	return f, nil
}

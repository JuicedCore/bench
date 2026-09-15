package fabricx

import (
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/protobuf/proto"
)

func txEnvelope(t *testing.T, txID string) []byte {
	t.Helper()
	ch, err := proto.Marshal(&common.ChannelHeader{TxId: txID, ChannelId: "arma"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(&common.Payload{Header: &common.Header{ChannelHeader: ch}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := proto.Marshal(&common.Envelope{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// The sidecar's TRANSACTIONS_FILTER bytes are committerpb.Status values, where
// COMMITTED is 1. Reading them with Fabric's 0 == VALID convention rejected every
// committed transaction on a live network.
func TestDecodeBlockUsesCommitterStatus(t *testing.T) {
	md := make([][]byte, int(common.BlockMetadataIndex_TRANSACTIONS_FILTER)+1)
	md[common.BlockMetadataIndex_TRANSACTIONS_FILTER] = []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
		byte(committerpb.Status_STATUS_UNSPECIFIED),
	}
	blk := &common.Block{
		Header:   &common.BlockHeader{Number: 7},
		Data:     &common.BlockData{Data: [][]byte{txEnvelope(t, "ok"), txEnvelope(t, "mvcc"), txEnvelope(t, "unset")}},
		Metadata: &common.BlockMetadata{Metadata: md},
	}

	out := decodeBlock(blk, time.Now())
	if len(out) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(out))
	}
	want := map[string]bool{"ok": true, "mvcc": false, "unset": false}
	for _, o := range out {
		if o.valid != want[o.txID] {
			t.Errorf("tx %s: valid=%v (status %s), want %v", o.txID, o.valid, committerpb.Status(o.code), want[o.txID])
		}
		if o.blockNum != 7 {
			t.Errorf("tx %s: blockNum=%d, want 7", o.txID, o.blockNum)
		}
	}
	if r := finality(out[1]); r.InvalidReason != "fabricx status ABORTED_MVCC_CONFLICT" {
		t.Errorf("InvalidReason = %q", r.InvalidReason)
	}
}

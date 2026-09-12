package drunix

import "encoding/json"

// Drunix's YugabyteDB statedb (its shipped test-network default, see
// docs/platforms/drunix.md) force-casts every non-lifecycle write value into a
// JSONB column and panics the Committing Peer if the value isn't valid JSON
// (core/ledger/kvledger/txmgmt/statedb/statesqldb/sql_client.go in the drunix
// source: it swallows the json.Unmarshal error and inserts anyway). The kv
// workload's raw byte payloads (pkg/workloads) trip this on every write.
//
// wrapValue/unwrapValue encode the payload as a JSON string so it survives the
// JSONB cast, keeping the write byte-for-byte recoverable. Plain string (not
// base64) is sufficient because the shared workload only ever emits printable
// ASCII; switch to base64 here if that generator ever emits arbitrary binary.
// This is disclosed as a manifest caveat (cmd/benchrunner/main.go) since the
// on-wire payload no longer matches the shared workload's raw bytes on other
// platforms.

func wrapValue(v []byte) []byte {
	out, _ := json.Marshal(string(v)) // Marshal of a string never errors
	return out
}

func unwrapValue(v []byte) []byte {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return v // not our wrapped format; pass through unchanged
	}
	return []byte(s)
}

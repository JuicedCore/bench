// The custom Fabric-X "kv-write" FSC view service.
//
// Builds STDLIB-ONLY today: main.go serves /healthz and /kv, /kv returns 501.
// The real implementation needs the fabric-x-samples FSC SDK wiring
// (github.com/hyperledger/fabric-samples/token-sdk/common: StartFSC, NewSDK,
// WithAnyCORS) + a devnet namespace registered with the committer. Both require
// a running Fabric-X devnet to build+test against; the exact FSC call sequence
// is pinned in README.md. Keep this module stdlib-only until that work lands so
// the container image stays trivial to build.
module github.com/juicedcore/bench/deploy/docker/fabricx/kvview

go 1.23

# ADR-001: Go for the whole harness

## Context

Four platforms in three languages' ecosystems: Fabric/Drunix/Fabric-X (Go),
NeuChain (C++). The harness needs one load generator, one metrics pipeline, one
adapter interface.

## Decision

Write the entire harness in Go. Every adapter — including NeuChain's — is Go.
NeuChain is reached over gRPC from generated Go stubs (or by wrapping its native
client), never via cgo.

## Rationale

- Fabric's Gateway SDK is Go-first; the Fabric/Drunix adapters are near-trivial.
- Go's goroutines + `context` make an open/closed-loop generator with thousands
  of in-flight transactions straightforward.
- One toolchain, one `go test`, one binary to ship.
- NeuChain exposes gRPC; a Go client needs only the `.proto` files.

## Alternatives considered

- **Rust** — good concurrency, but no first-party Fabric SDK; would need raw gRPC
  against the gateway proto.
- **Polyglot (Go for Fabric, C++ for NeuChain)** — two build systems, two test
  frameworks, shared result format to keep in sync. Rejected.
- **cgo to link NeuChain client code** — build complexity, cross-compilation
  pain, and it drags C++ toolchain requirements into the harness. Rejected
  ([adr-002](adr-002-neuchain-client-spike.md)).

## Consequences

- NeuChain integration risk concentrates in one place: reproducing its tx format
  in Go, mitigated by the proto spike and the native-binary fallback.
- The NeuChain *server* still needs its C++ build (Ubuntu 20.04 container); only
  the client is Go.

// Package adapters defines the platform-agnostic contract that every blockchain
// under test must satisfy. The harness only ever talks to a PlatformAdapter, so
// the load generator and metrics pipeline are identical across Fabric, Fabric-X,
// NeuChain and Drunix. See docs/architecture/overview.md for the model and
// docs/architecture/metrics-methodology.md for why the timestamps are split the
// way they are.
package adapters

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrFinalityTimeout is wrapped by adapters when WaitForFinality gave up because
// the per-transaction timeout elapsed. The load generator counts it as timed out.
var ErrFinalityTimeout = errors.New("finality timeout")

// ErrFinalityStreamDown is wrapped by adapters when WaitForFinality cannot
// succeed because the block/commit stream that observes finality has failed and
// could not be re-established. The load generator counts it as an error, not a
// timeout, so a dead listener is not mistaken for a slow platform.
var ErrFinalityStreamDown = errors.New("finality stream down")

// ErrNotSetUp is returned by adapter methods called before a successful Setup.
var ErrNotSetUp = errors.New("adapter not set up (Setup failed or was not called)")

// PlatformAdapter is the single abstraction that makes cross-architecture
// benchmarking fair: every platform exposes the same submit -> wait-for-finality
// lifecycle regardless of its internal transaction model (EOV with chaincode,
// EOV without chaincode, or ordering-free EV).
//
// Implementations MUST be safe for concurrent use by many load-generator
// goroutines after Setup returns.
type PlatformAdapter interface {
	// Name is the stable identifier used in config files, results paths and
	// reports (e.g. "fabric-cft", "fabric-bft", "drunix", "fabricx", "neuchain").
	Name() string

	// Setup establishes client connections, loads identities, and verifies the
	// target network is reachable and the workload contract/view is installed.
	// It MUST NOT deploy the network itself - deployment is done out of band by
	// deploy/docker + the benchrunner "setup" command.
	Setup(ctx context.Context, cfg AdapterConfig) error

	// Teardown releases all client-side resources. It MUST NOT tear down the
	// network. Safe to call even if Setup partially failed, and safe to call
	// more than once. The engine calls it after a failed Setup too.
	Teardown(ctx context.Context) error

	// Submit hands one transaction to the platform. It MUST return as soon as the
	// platform has acknowledged receipt (T2) and MUST NOT block until finality.
	// For platforms whose SDK only offers a synchronous submit, the adapter is
	// responsible for using the lower-level async API (e.g. Fabric Gateway
	// Endorse -> Submit -> CommitStatus) so that T2 and T3 stay distinct.
	Submit(ctx context.Context, tx *Transaction) (*SubmitResult, error)

	// WaitForFinality blocks until the transaction identified by txID is
	// committed to the ledger (T3) or the timeout elapses. A committed-but-invalid
	// transaction (e.g. MVCC read conflict) returns a FinalityResult with
	// Valid=false and no error - the harness counts it as a failure, not
	// throughput.
	WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*FinalityResult, error)

	// Query reads current world state for a key without generating ledger load.
	// Used by workload verification, not by the throughput measurement.
	Query(ctx context.Context, key string) (*QueryResult, error)

	// MetricsEndpoint returns the platform's own Prometheus scrape URL, or "" if
	// it exposes none. These metrics are informational only and are never used in
	// cross-platform comparison (see docs/architecture/fairness-guarantees.md).
	MetricsEndpoint() string
}

// TxKind enumerates the normalized operation the workload asked for. The adapter
// maps it onto whatever the platform actually does (chaincode invoke, FSC view,
// native gRPC call).
type TxKind int

const (
	// TxWrite sets a single key to a value.
	TxWrite TxKind = iota
	// TxRead reads a single key.
	TxRead
	// TxTransfer moves an amount from one account key to another.
	TxTransfer
)

func (k TxKind) String() string {
	switch k {
	case TxWrite:
		return "write"
	case TxRead:
		return "read"
	case TxTransfer:
		return "transfer"
	default:
		return "unknown"
	}
}

// Transaction is the platform-neutral description of one unit of work produced by
// a workload. The adapter translates it into a platform-specific call.
type Transaction struct {
	// Kind is the normalized operation.
	Kind TxKind

	// Key is the primary key touched (TxWrite, TxRead) or the source account
	// (TxTransfer).
	Key string

	// DestKey is the destination account for TxTransfer, empty otherwise.
	DestKey string

	// Value is the payload for TxWrite. For TxTransfer, Amount is used instead.
	Value []byte

	// Amount is the transfer amount for TxTransfer.
	Amount int64

	// Seq is the monotonically increasing index assigned by the load generator.
	// Used only for logging and correlation; not sent to the platform.
	Seq uint64
}

// AdapterConfig is the adapter-specific slice of a run configuration. The harness
// passes the raw map through from YAML; each adapter decodes the keys it needs.
// Common keys are surfaced as typed fields; everything else stays in Extra.
type AdapterConfig struct {
	// Profile is the resource profile name ("local", "gcp-small", "gcp-full").
	Profile string

	// Workload is the workload name ("kv-write", "kv-read", "transfer", or a
	// platform-native name).
	Workload string

	// Normalized is true for the cross-platform comparison runs (identical
	// config, LevelDB, pinned orderer batch params) and false for
	// platform-native best-case runs. See docs/decisions/adr-013-config-parity-policy.md.
	Normalized bool

	// ConnProfilePath points at the platform's client connection material
	// (Fabric connection profile, FSC/REST endpoint file, NeuChain endpoint list).
	ConnProfilePath string

	// Extra carries platform-specific keys verbatim from YAML.
	Extra map[string]any

	// Logger receives adapter diagnostics that cannot be returned as an error:
	// background stream failures, reconnects, ignored config keys. It already
	// carries the platform attribute and is teed into the run's run.log. nil
	// means slog.Default().
	Logger *slog.Logger
}

// Log returns cfg.Logger, or slog.Default() when unset.
func (cfg AdapterConfig) Log() *slog.Logger {
	if cfg.Logger != nil {
		return cfg.Logger
	}
	return slog.Default()
}

// SubmitResult records the two client-observable timestamps around submission.
// See docs/architecture/metrics-methodology.md for the T1/T2/T3 timeline.
type SubmitResult struct {
	// TxID is the platform-assigned (or adapter-assigned) transaction id used to
	// correlate with WaitForFinality.
	TxID string

	// SubmitTime (T1) is captured by the adapter immediately before the first
	// network call for this transaction.
	SubmitTime time.Time

	// AckTime (T2) is captured when the platform acknowledges receipt: for Fabric
	// this is after endorsement + broadcast accept; for NeuChain after the submit
	// RPC returns; for Fabric-X after the FSC view accepts the request.
	AckTime time.Time
}

// FinalityResult records commit-time facts about a transaction.
type FinalityResult struct {
	// TxID matches the SubmitResult.
	TxID string

	// FinalityTime (T3) is captured when the adapter observes the transaction in
	// a committed block (event, stream, or poll - documented per adapter).
	FinalityTime time.Time

	// BlockNum is the ledger height at which the transaction committed, or 0 if
	// the platform does not expose it.
	BlockNum uint64

	// Valid is false if the transaction committed but was marked invalid by
	// validation (MVCC conflict, endorsement policy failure, double-spend).
	Valid bool

	// InvalidReason, when Valid is false, is the platform's validation code or
	// message (e.g. "MVCC_READ_CONFLICT"), so failed phases group invalid
	// transactions by cause rather than as one opaque "committed invalid".
	InvalidReason string
}

// QueryResult is the outcome of a state read.
type QueryResult struct {
	Key   string
	Value []byte
	Found bool
}

// CryptoInfo is an adapter's self-description of its signing / hashing config.
// An adapter MAY implement `CryptoInfo() CryptoInfo`; the engine copies the
// result into the run manifest so EOV-vs-EV differences (per-transaction
// endorsement signature verification vs none) are disclosed alongside results
// rather than hidden. See docs/architecture/fairness-guarantees.md.
type CryptoInfo struct {
	SignatureAlg           string `json:"signature_alg"` // "ECDSA-P256", "ed25519", "none"
	HashAlg                string `json:"hash_alg"`      // "SHA-256"
	PerTxEndorsementVerify bool   `json:"per_tx_endorsement_verify"`
	MSPNote                string `json:"msp_note,omitempty"`
}

// VersionReporter is an optional adapter capability: report the platform's
// build/image tag for the manifest.
type VersionReporter interface {
	PlatformVersion() string
}

// CryptoReporter is an optional adapter capability: describe signing config.
type CryptoReporter interface {
	CryptoInfo() CryptoInfo
}

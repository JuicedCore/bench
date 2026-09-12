// Package drunix is the PlatformAdapter for NPCI's Drunix, an enhanced fork of
// Hyperledger Fabric 2.5.x. Drunix splits the peer into a Lite Peer (endorsement)
// and a Committing Peer (validation + commit) and adds a stateless Validation
// Service, but keeps the Fabric Gateway SDK API surface. This adapter therefore
// delegates to the fabric adapter, differing only in defaults and labelling.
//
// See docs/platforms/drunix.md and docs/decisions/adr-007-drunix-cft-only.md.
package drunix

import (
	"context"
	"errors"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
	"github.com/juicedcore/bench/pkg/adapters/fabric"
)

const platformName = "drunix"

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter wraps a fabric.Adapter configured for the Drunix Lite Peer / Committing
// Peer topology.
type Adapter struct {
	inner *fabric.Adapter
}

func (a *Adapter) Name() string { return platformName }

// Setup builds the fabric config with Drunix defaults, then delegates.
func (a *Adapter) Setup(ctx context.Context, ac adapters.AdapterConfig) error {
	cfg, err := fabric.ConfigFor(platformName, ac.Extra)
	if err != nil {
		return err
	}
	// If only peer_endpoint was supplied, treat it as the Lite Peer for
	// endorsement.
	if cfg.EndorseEndpoint == "" {
		cfg.EndorseEndpoint = cfg.PeerEndpoint
	}
	// Drunix's Gateway runs on the Lite Peer, which endorses + broadcasts but
	// never commits - so the Gateway's Commit.Status() never fires. Read finality
	// from the Committing Peer's block-event stream instead.
	cfg.UseCommitPeerEvents = true
	if cfg.CommitEndpoint == "" {
		cfg.CommitEndpoint = cfg.EndorseEndpoint
	}
	a.inner = fabric.NewWithConfig(platformName, cfg)
	return a.inner.Setup(ctx, ac)
}

// errNotSetUp guards the delegating methods below. The PlatformAdapter contract
// requires Teardown to be safe after a failed or skipped Setup, and the engine's
// deferred Teardown runs on exactly that path; without the guard every one of
// these nil-derefs a.inner.
var errNotSetUp = errors.New("drunix: adapter not set up")

func (a *Adapter) Teardown(ctx context.Context) error {
	if a.inner == nil {
		return nil
	}
	return a.inner.Teardown(ctx)
}

func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	if a.inner == nil {
		return nil, errNotSetUp
	}
	if tx.Kind == adapters.TxWrite {
		// See valuecodec.go: Drunix's YugabyteDB statedb panics the Committing
		// Peer on non-JSON write values.
		wrapped := *tx
		wrapped.Value = wrapValue(tx.Value)
		return a.inner.Submit(ctx, &wrapped)
	}
	return a.inner.Submit(ctx, tx)
}

func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	if a.inner == nil {
		return nil, errNotSetUp
	}
	return a.inner.WaitForFinality(ctx, txID, timeout)
}

func (a *Adapter) Query(ctx context.Context, key string) (*adapters.QueryResult, error) {
	if a.inner == nil {
		return nil, errNotSetUp
	}
	res, err := a.inner.Query(ctx, key)
	if err != nil || res == nil || !res.Found {
		return res, err
	}
	unwrapped := *res
	unwrapped.Value = unwrapValue(res.Value)
	return &unwrapped, nil
}

func (a *Adapter) MetricsEndpoint() string {
	if a.inner == nil {
		return ""
	}
	return a.inner.MetricsEndpoint()
}

// PlatformVersion / CryptoInfo are safe to call before Setup (the engine reads
// them while building the manifest).
func (a *Adapter) PlatformVersion() string { return platformName }

func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote:                "Drunix (HLF 2.5.x fork); Lite Peer endorses, Committing Peer validates+commits",
	}
}

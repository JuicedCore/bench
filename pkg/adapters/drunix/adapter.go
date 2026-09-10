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
	// endorsement (the Gateway SDK opens one connection there; the Lite Peer's
	// gateway federates commit-status from the Committing Peer).
	if cfg.EndorseEndpoint == "" {
		cfg.EndorseEndpoint = cfg.PeerEndpoint
	}
	a.inner = fabric.NewWithConfig(platformName, cfg)
	return a.inner.Setup(ctx, ac)
}

func (a *Adapter) Teardown(ctx context.Context) error { return a.inner.Teardown(ctx) }

func (a *Adapter) Submit(ctx context.Context, tx *adapters.Transaction) (*adapters.SubmitResult, error) {
	return a.inner.Submit(ctx, tx)
}

func (a *Adapter) WaitForFinality(ctx context.Context, txID string, timeout time.Duration) (*adapters.FinalityResult, error) {
	return a.inner.WaitForFinality(ctx, txID, timeout)
}

func (a *Adapter) Query(ctx context.Context, key string) (*adapters.QueryResult, error) {
	return a.inner.Query(ctx, key)
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

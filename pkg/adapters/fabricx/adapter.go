// Package fabricx is the PlatformAdapter for Fabric-X (Arma ordering + FSC views
// + Token SDK). PHASE 3 - skeleton only.
//
// Design (see docs/platforms/fabric-x.md and
// docs/decisions/adr-003-fabricx-fsc-view-and-rest.md):
//
//   - normalized kv-* -> a custom minimal FSC "kv-write" view
//   - native token-transfer -> Token SDK Issue/Transfer/Redeem over the REST API
//   - Submit returns when the FSC view accepts the request (T2); WaitForFinality
//     blocks on a committer commit event (T3)
//   - the FSC client node + REST server sit in the measured path and are
//     disclosed in the manifest, not "corrected"
package fabricx

import (
	"context"
	"errors"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "fabricx"

// errPhase3 is returned by every method until the adapter is implemented.
var errPhase3 = errors.New("fabricx adapter is Phase 3 - not implemented yet (see docs/decisions/adr-015-phased-delivery.md)")

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter is the Fabric-X adapter skeleton.
type Adapter struct{}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) MetricsEndpoint() string { return "" }
func (a *Adapter) PlatformVersion() string { return platformName }

// CryptoInfo is known ahead of implementation: Fabric-X keeps X.509 MSP identity
// and per-transaction signature verification in the committer.
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "ECDSA-P256",
		HashAlg:                "SHA-256",
		PerTxEndorsementVerify: true,
		MSPNote:                "Fabric-X: FSC view/session + Token SDK; committer verifies signatures",
	}
}

func (a *Adapter) Setup(context.Context, adapters.AdapterConfig) error { return errPhase3 }
func (a *Adapter) Teardown(context.Context) error                     { return nil }

func (a *Adapter) Submit(context.Context, *adapters.Transaction) (*adapters.SubmitResult, error) {
	return nil, errPhase3
}

func (a *Adapter) WaitForFinality(context.Context, string, time.Duration) (*adapters.FinalityResult, error) {
	return nil, errPhase3
}

func (a *Adapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return nil, errPhase3
}

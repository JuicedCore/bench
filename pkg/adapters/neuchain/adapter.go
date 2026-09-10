// Package neuchain is the PlatformAdapter for NeuChain (ordering-free EV, C++,
// gRPC). PHASE 4 - skeleton only.
//
// Client strategy is decided by a time-boxed proto spike
// (docs/decisions/adr-002-neuchain-client-spike.md); the outcome and the exact
// Go<->C++ mapping are written up in
// docs/platforms/neuchain-client-implementation.md before this skeleton is
// fleshed out. Generated stubs land in pkg/adapters/neuchain/proto/.
package neuchain

import (
	"context"
	"errors"
	"time"

	"github.com/juicedcore/bench/pkg/adapters"
)

const platformName = "neuchain"

var errPhase4 = errors.New("neuchain adapter is Phase 4 - not implemented yet; run the proto spike first (adr-002)")

func init() {
	adapters.Register(platformName, func() adapters.PlatformAdapter { return &Adapter{} })
}

// Adapter is the NeuChain adapter skeleton.
type Adapter struct{}

func (a *Adapter) Name() string            { return platformName }
func (a *Adapter) MetricsEndpoint() string { return "" }
func (a *Adapter) PlatformVersion() string { return platformName }

// CryptoInfo declares the key EOV-vs-EV asymmetry up front: NeuChain's
// deterministic EV path has no per-transaction endorsement signature check.
func (a *Adapter) CryptoInfo() adapters.CryptoInfo {
	return adapters.CryptoInfo{
		SignatureAlg:           "tbd-by-spike",
		HashAlg:                "tbd-by-spike",
		PerTxEndorsementVerify: false,
		MSPNote:                "NeuChain EV: deterministic execution, no per-tx endorsement signatures",
	}
}

func (a *Adapter) Setup(context.Context, adapters.AdapterConfig) error { return errPhase4 }
func (a *Adapter) Teardown(context.Context) error                     { return nil }

func (a *Adapter) Submit(context.Context, *adapters.Transaction) (*adapters.SubmitResult, error) {
	return nil, errPhase4
}

func (a *Adapter) WaitForFinality(context.Context, string, time.Duration) (*adapters.FinalityResult, error) {
	return nil, errPhase4
}

func (a *Adapter) Query(context.Context, string) (*adapters.QueryResult, error) {
	return nil, errPhase4
}

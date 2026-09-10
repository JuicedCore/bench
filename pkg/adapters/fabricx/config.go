package fabricx

import (
	"fmt"
	"time"
)

// Config is the Fabric-X adapter configuration, decoded from the run config's
// `adapter:` map.
//
// The adapter is an HTTP client for the fabric-x-samples "tokens" REST services
// plus one custom route:
//
//   - native transfer  -> POST {OwnerURL}/owner/accounts/{sender}/transfer   (real, from swagger.yaml)
//   - native issue      -> POST {IssuerURL}/issuer/issue                      (real)
//   - normalized kv     -> POST {KVURL}/kv                                    (custom FSC view, deploy/docker/fabricx/kvview)
//   - state read        -> GET  {OwnerURL}/owner/accounts/{id}?code=<type>    (real)
//
// The token transfer/issue POST is SYNCHRONOUS to finality
// (service/fsc.go runs ttx.NewOrderingAndFinalityView before returning), so for
// Fabric-X there is no separable submit-ack (T2). The adapter reports T1 and T3;
// submit latency is N/A. See docs/architecture/fairness-guarantees.md and adr-003.
type Config struct {
	// OwnerURL fronts the owner service (swagger default :9500 for alice/bob).
	OwnerURL string `yaml:"owner_url"`
	// IssuerURL fronts the issuer service (swagger default :9100).
	IssuerURL string `yaml:"issuer_url"`
	// KVURL fronts the custom kv-write view service (deploy/docker/fabricx/kvview).
	KVURL string `yaml:"kv_url"`

	// SenderAccount is the {id} path segment for owner routes (e.g. "alice").
	SenderAccount string `yaml:"sender_account"`
	// CounterpartyNode is the FSC node holding the recipient account (e.g. "owner1").
	CounterpartyNode string `yaml:"counterparty_node"`
	// TokenCode is the token type for transfer/issue amounts (swagger default "EURX").
	TokenCode string `yaml:"token_code"`

	// MetricsEndpointURL is the committer's /metrics (informational only).
	MetricsEndpointURL string `yaml:"metrics_endpoint"`

	HTTPTimeout time.Duration `yaml:"http_timeout"`
}

func (c *Config) applyDefaults() {
	if c.OwnerURL == "" {
		c.OwnerURL = "http://localhost:9500"
	}
	if c.IssuerURL == "" {
		c.IssuerURL = "http://localhost:9100"
	}
	if c.KVURL == "" {
		c.KVURL = "http://localhost:9700"
	}
	if c.SenderAccount == "" {
		c.SenderAccount = "alice"
	}
	if c.CounterpartyNode == "" {
		c.CounterpartyNode = "owner1"
	}
	if c.TokenCode == "" {
		c.TokenCode = "EURX"
	}
	if c.HTTPTimeout == 0 {
		// generous: the transfer POST blocks to finality.
		c.HTTPTimeout = 2 * time.Minute
	}
}

func (c *Config) validate() error {
	if c.OwnerURL == "" && c.KVURL == "" {
		return fmt.Errorf("fabricx: owner_url or kv_url is required")
	}
	return nil
}

func configFromExtra(extra map[string]any) (*Config, error) {
	c := &Config{}
	if extra != nil {
		str := func(k string) string { s, _ := extra[k].(string); return s }
		c.OwnerURL = str("owner_url")
		c.IssuerURL = str("issuer_url")
		c.KVURL = str("kv_url")
		if s := str("sender_account"); s != "" {
			c.SenderAccount = s
		}
		if s := str("counterparty_node"); s != "" {
			c.CounterpartyNode = s
		}
		if s := str("token_code"); s != "" {
			c.TokenCode = s
		}
		c.MetricsEndpointURL = str("metrics_endpoint")
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

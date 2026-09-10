package fabricx

import (
	"fmt"
	"time"
)

// Config is the Fabric-X adapter's connection + endpoint configuration, decoded
// from the run config's `adapter:` map.
//
// The adapter talks HTTP to the REST façade the Fabric-X tokens sample exposes
// in front of an FSC client node (see
// docs/decisions/adr-003-fabricx-fsc-view-and-rest.md). The exact routes below
// are an ASSUMED contract - they are all overridable so they can be corrected
// against the real sample without touching adapter logic.
type Config struct {
	// BaseURL of the REST façade, e.g. "http://localhost:8080".
	BaseURL string `yaml:"base_url"`

	// Routes. %s in TxStatusRoute / TxWaitRoute is replaced with the tx id.
	KVRoute       string `yaml:"kv_route"`        // POST: normalized kv-* via a custom FSC view
	TransferRoute string `yaml:"transfer_route"` // POST: native Token SDK transfer
	TxStatusRoute string `yaml:"tx_status_route"` // GET:  poll commit status
	TxWaitRoute   string `yaml:"tx_wait_route"`   // GET:  optional long-poll to finality ("" disables)

	// FinalityMode: "poll" or "longpoll". longpoll uses TxWaitRoute; poll loops
	// TxStatusRoute every PollInterval.
	FinalityMode string        `yaml:"finality_mode"`
	PollInterval time.Duration `yaml:"poll_interval"`

	HTTPTimeout time.Duration `yaml:"http_timeout"`

	// MetricsEndpointURL is the committer's /metrics (informational only).
	MetricsEndpointURL string `yaml:"metrics_endpoint"`

	// Owner identities for the token workload; if empty the adapter derives
	// "acct-<n>" names from the transaction keys.
	TokenType string `yaml:"token_type"`
}

func (c *Config) applyDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:8080"
	}
	if c.KVRoute == "" {
		c.KVRoute = "/api/v1/kv"
	}
	if c.TransferRoute == "" {
		c.TransferRoute = "/api/v1/tokens/transfer"
	}
	if c.TxStatusRoute == "" {
		c.TxStatusRoute = "/api/v1/tx/%s"
	}
	if c.TxWaitRoute == "" {
		c.TxWaitRoute = "/api/v1/tx/%s/wait"
	}
	if c.FinalityMode == "" {
		c.FinalityMode = "poll"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 100 * time.Millisecond
	}
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 30 * time.Second
	}
	if c.TokenType == "" {
		c.TokenType = "BENCH"
	}
}

func (c *Config) validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("fabricx: base_url is required")
	}
	switch c.FinalityMode {
	case "poll", "longpoll":
	default:
		return fmt.Errorf("fabricx: finality_mode must be poll or longpoll, got %q", c.FinalityMode)
	}
	if c.FinalityMode == "longpoll" && c.TxWaitRoute == "" {
		return fmt.Errorf("fabricx: finality_mode=longpoll needs tx_wait_route")
	}
	return nil
}

func configFromExtra(extra map[string]any) (*Config, error) {
	c := &Config{}
	str := func(k string) (string, bool) {
		if extra == nil {
			return "", false
		}
		v, ok := extra[k].(string)
		return v, ok
	}
	for k, dst := range map[string]*string{
		"base_url": &c.BaseURL, "kv_route": &c.KVRoute, "transfer_route": &c.TransferRoute,
		"tx_status_route": &c.TxStatusRoute, "tx_wait_route": &c.TxWaitRoute,
		"finality_mode": &c.FinalityMode, "metrics_endpoint": &c.MetricsEndpointURL,
		"token_type": &c.TokenType,
	} {
		if v, ok := str(k); ok {
			*dst = v
		}
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

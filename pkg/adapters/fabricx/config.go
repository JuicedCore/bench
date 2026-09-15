// Package fabricx is the PlatformAdapter for Hyperledger Fabric-X.
//
// Transactions are submitted by broadcasting a signed envelope to an Arma
// router over gRPC, and outcomes are read from the sidecar's deliver stream.
// Those are separate operations on separate connections, so unlike the previous
// REST-based integration this adapter observes the router's own acknowledgement
// of each envelope (T2), distinct from finality (T3). See the broadcaster in
// stream.go for how replies are matched to envelopes.
//
// Namespace bootstrap is NOT done here - it is a deploy-time concern handled by
// deploy/docker/fabricx/up.sh, which registers the namespace's verification key
// in the `_meta` namespace before the benchmark starts. This adapter only needs
// the matching signing key.
//
// See docs/platforms/fabricx-integration.md.
package fabricx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the Fabric-X adapter configuration, decoded from the run config's
// `adapter:` map.
type Config struct {
	// BroadcastEndpoints are the Arma routers' host:port, one per party, given as
	// a comma-separated broadcast_endpoint. Every envelope goes to all of them -
	// see routerSet in stream.go for why a single router is ~10s slower.
	BroadcastEndpoints []string `yaml:"broadcast_endpoint"`
	// DeliverEndpoint is where committed blocks are read from. The sidecar is
	// preferred: it delivers blocks carrying per-transaction validation codes,
	// which is what finality is decided on.
	DeliverEndpoint string `yaml:"deliver_endpoint"`

	// ChannelID is the Arma channel. armageddon hardcodes "arma".
	ChannelID string `yaml:"channel_id"`
	// Namespace is the application namespace transactions are written to.
	Namespace string `yaml:"namespace"`
	// SigningKeyPath is a PKCS#8 PEM ECDSA private key whose public half was
	// registered as the namespace's policy at deploy time. Signatures are
	// rejected if the two do not match.
	SigningKeyPath string `yaml:"signing_key_path"`

	// MetricsEndpointURL is the committer's /metrics, scraped once at end of run
	// for the native (never cross-platform) section.
	MetricsEndpointURL string `yaml:"metrics_endpoint"`

	// DialTimeout bounds connection setup in Setup.
	DialTimeout time.Duration `yaml:"dial_timeout"`

	// BroadcastStreams is how many broadcast streams are kept open. Each carries
	// at most one unacknowledged envelope, so this caps transactions awaiting the
	// router's reply, not total throughput - an ack normally arrives in well under
	// a millisecond after the router forwards to a batcher. If it ever binds, the
	// load generator falls behind schedule and send-gap rejects the step, so it
	// cannot silently cap a measurement.
	BroadcastStreams int `yaml:"broadcast_streams"`
	// AckTimeout bounds the wait for the router's reply to one envelope.
	AckTimeout time.Duration `yaml:"ack_timeout"`
}

func (c *Config) applyDefaults() {
	if c.ChannelID == "" {
		c.ChannelID = "arma"
	}
	if c.Namespace == "" {
		c.Namespace = "0"
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.BroadcastStreams <= 0 {
		c.BroadcastStreams = 256
	}
	if c.AckTimeout == 0 {
		c.AckTimeout = 30 * time.Second
	}
}

func (c *Config) validate() error {
	const hint = "is deploy/docker/fabricx/connection.env sourced?"
	var errs []error
	if len(c.BroadcastEndpoints) == 0 {
		errs = append(errs, fmt.Errorf("fabricx: adapter.broadcast_endpoint is required (comma-separated Arma router host:port, one per party; %s)", hint))
	}
	if c.DeliverEndpoint == "" {
		errs = append(errs, fmt.Errorf("fabricx: adapter.deliver_endpoint is required (sidecar host:port; %s)", hint))
	}
	if c.SigningKeyPath == "" {
		errs = append(errs, fmt.Errorf("fabricx: adapter.signing_key_path is required; deploy/docker/fabricx/up.sh emits it (%s)", hint))
	}
	if c.AckTimeout <= 0 {
		errs = append(errs, fmt.Errorf("fabricx: adapter.ack_timeout must be > 0, got %s", c.AckTimeout))
	}
	if c.DialTimeout <= 0 {
		errs = append(errs, fmt.Errorf("fabricx: adapter.dial_timeout must be > 0, got %s", c.DialTimeout))
	}
	return errors.Join(errs...)
}

// configFromExtra decodes the run config's `adapter:` map. Keys belonging to
// other platforms are ignored, and an unset ${VAR} arrives as the empty string
// rather than being absent - so "" always means unset, never a real value. That
// is what lets one shared normalized config serve every platform.
func configFromExtra(extra map[string]any) (*Config, error) {
	c := &Config{}
	if extra != nil {
		str := func(k string) string { s, _ := extra[k].(string); return s }
		for _, ep := range strings.Split(str("broadcast_endpoint"), ",") {
			if ep = strings.TrimSpace(ep); ep != "" {
				c.BroadcastEndpoints = append(c.BroadcastEndpoints, ep)
			}
		}
		c.DeliverEndpoint = str("deliver_endpoint")
		c.SigningKeyPath = str("signing_key_path")
		c.MetricsEndpointURL = str("metrics_endpoint")
		if s := str("channel_id"); s != "" {
			c.ChannelID = s
		}
		if s := str("namespace"); s != "" {
			c.Namespace = s
		}
		switch v := extra["broadcast_streams"].(type) {
		case int:
			c.BroadcastStreams = v
		case string:
			if v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("fabricx: bad broadcast_streams %q: %w", v, err)
				}
				c.BroadcastStreams = n
			}
		}
		if s := str("ack_timeout"); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("fabricx: bad ack_timeout %q: %w", s, err)
			}
			c.AckTimeout = d
		}
		if s := str("dial_timeout"); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("fabricx: bad dial_timeout %q: %w", s, err)
			}
			c.DialTimeout = d
		}
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

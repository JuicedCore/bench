package fabric

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config is the connection material the Fabric / Drunix adapters need. Every
// field is supplied via the run config's `adapter:` map, normally from the
// BENCH_ADAPTER_* variables in deploy/docker/<platform>/connection.env. Duration
// keys take Go duration strings ("15s", "2m").
type Config struct {
	// PeerEndpoint is the gRPC address used for BOTH endorsement and the gateway
	// (Fabric). For Drunix, EndorseEndpoint / CommitEndpoint override this to
	// point at the Lite Peer and Committing Peer respectively.
	PeerEndpoint    string `yaml:"peer_endpoint"`
	EndorseEndpoint string `yaml:"endorse_endpoint"`
	CommitEndpoint  string `yaml:"commit_endpoint"`

	// GatewayPeer is the TLS server-name override (certificate CN/SAN) for the
	// endorsing peer, e.g. "peer0.org1.example.com".
	GatewayPeer string `yaml:"gateway_peer"`

	MSPID         string `yaml:"msp_id"`
	CertPath      string `yaml:"cert_path"` // X509 signing cert (PEM)
	KeyPath       string `yaml:"key_path"`  // private key (PEM), or a keystore dir
	TLSCACertPath string `yaml:"tls_ca_cert_path"`

	Channel   string `yaml:"channel"`
	Chaincode string `yaml:"chaincode"`

	// MetricsEndpointURL is the platform's own Prometheus scrape URL. Optional;
	// informational only (never used in cross-platform comparison).
	MetricsEndpointURL string `yaml:"metrics_endpoint"`

	// Function names on the kvstore chaincode. Defaults match chaincodes/kvstore.
	FnPut      string `yaml:"fn_put"`
	FnGet      string `yaml:"fn_get"`
	FnTransfer string `yaml:"fn_transfer"`

	// EndorseTimeout / SubmitTimeout / CommitStatusTimeout bound each gateway call.
	EndorseTimeout      time.Duration `yaml:"endorse_timeout"`
	SubmitTimeout       time.Duration `yaml:"submit_timeout"`
	CommitStatusTimeout time.Duration `yaml:"commit_status_timeout"`

	// UseCommitPeerEvents makes WaitForFinality read tx validation from a
	// Committing Peer's filtered-block event stream instead of the Gateway's
	// Commit.Status(). Needed for Drunix: its Gateway runs on the (non-committing)
	// Lite Peer, so Commit.Status() never fires. CommitEndpoint (+ optional
	// CommitPeerGateway SNI) point at the CP; the TLS CA is shared with the LP.
	UseCommitPeerEvents bool   `yaml:"use_commit_peer_events"`
	CommitPeerGateway   string `yaml:"commit_peer_gateway"` // TLS server-name for the CP; defaults to GatewayPeer with peer0->peer1

	// PlatformName is the adapter name reported to the harness
	// ("fabric-cft", "fabric-bft", "drunix").
	PlatformName string `yaml:"-"`
}

func (c *Config) applyDefaults() {
	if c.Channel == "" {
		c.Channel = "mychannel"
	}
	if c.Chaincode == "" {
		c.Chaincode = "kvstore"
	}
	if c.FnPut == "" {
		c.FnPut = "Put"
	}
	if c.FnGet == "" {
		c.FnGet = "Get"
	}
	if c.FnTransfer == "" {
		c.FnTransfer = "Transfer"
	}
	if c.MSPID == "" {
		c.MSPID = "Org1MSP"
	}
	if c.EndorseEndpoint == "" {
		c.EndorseEndpoint = c.PeerEndpoint
	}
	if c.CommitEndpoint == "" {
		c.CommitEndpoint = c.EndorseEndpoint
	}
	if c.EndorseTimeout == 0 {
		c.EndorseTimeout = 15 * time.Second
	}
	if c.SubmitTimeout == 0 {
		c.SubmitTimeout = 15 * time.Second
	}
	if c.CommitStatusTimeout == 0 {
		c.CommitStatusTimeout = 2 * time.Minute
	}
}

// envHint is appended to config errors: an empty required key almost always
// means the platform's connection.env was not sourced into the environment.
const envHint = "is connection.env sourced? set -a; source deploy/docker/<platform>/connection.env; set +a"

func (c *Config) validate() error {
	p := c.PlatformName
	if p == "" {
		p = "fabric"
	}
	var errs []error
	if c.EndorseEndpoint == "" {
		errs = append(errs, fmt.Errorf("%s: adapter.peer_endpoint (or endorse_endpoint) is empty (%s)", p, envHint))
	}
	if c.CertPath == "" || c.KeyPath == "" {
		errs = append(errs, fmt.Errorf("%s: adapter.cert_path and adapter.key_path are required (%s)", p, envHint))
	}
	if c.TLSCACertPath == "" {
		errs = append(errs, fmt.Errorf("%s: adapter.tls_ca_cert_path is required (%s)", p, envHint))
	}
	for key, path := range map[string]string{"cert_path": c.CertPath, "key_path": c.KeyPath, "tls_ca_cert_path": c.TLSCACertPath} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("%s: adapter.%s %s: %w (crypto material is regenerated on every deploy; re-source connection.env after redeploying)", p, key, path, err))
		}
	}
	for key, d := range map[string]time.Duration{"endorse_timeout": c.EndorseTimeout, "submit_timeout": c.SubmitTimeout, "commit_status_timeout": c.CommitStatusTimeout} {
		if d < 0 {
			errs = append(errs, fmt.Errorf("%s: adapter.%s must not be negative, got %s", p, key, d))
		}
	}
	return errors.Join(errs...)
}

// ConfigFor decodes an adapter map into a validated Config. Exported for adapters
// that reuse this implementation (e.g. drunix).
func ConfigFor(name string, extra map[string]any) (*Config, error) {
	return configFromExtra(name, extra)
}

// configFromExtra decodes the adapter map from the run config into a Config.
func configFromExtra(name string, extra map[string]any) (*Config, error) {
	c := &Config{PlatformName: name}
	var errs []error
	get := func(k string) (string, bool) {
		raw, present := extra[k]
		if !present || raw == nil {
			return "", false
		}
		v, ok := raw.(string)
		if !ok {
			// YAML turned it into a number/bool; take its text form rather than
			// silently dropping the key.
			v = fmt.Sprint(raw)
			slog.Debug("adapter key is not a string; using its text form", "platform", name, "key", k, "value", v)
		}
		return v, true
	}
	getDur := func(k string, dst *time.Duration) {
		if v, ok := get(k); ok && v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: adapter.%s %q: %w (want e.g. 15s)", name, k, v, err))
				return
			}
			*dst = d
		}
	}
	if v, ok := get("fn_put"); ok && v != "" {
		c.FnPut = v
	}
	if v, ok := get("fn_get"); ok && v != "" {
		c.FnGet = v
	}
	if v, ok := get("fn_transfer"); ok && v != "" {
		c.FnTransfer = v
	}
	getDur("endorse_timeout", &c.EndorseTimeout)
	getDur("submit_timeout", &c.SubmitTimeout)
	getDur("commit_status_timeout", &c.CommitStatusTimeout)
	if v, ok := get("use_commit_peer_events"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: adapter.use_commit_peer_events %q: want true or false", name, v))
		}
		c.UseCommitPeerEvents = b
	}
	if v, ok := get("commit_peer_gateway"); ok {
		c.CommitPeerGateway = v
	}
	if v, ok := get("peer_endpoint"); ok {
		c.PeerEndpoint = v
	}
	if v, ok := get("endorse_endpoint"); ok {
		c.EndorseEndpoint = v
	}
	if v, ok := get("commit_endpoint"); ok {
		c.CommitEndpoint = v
	}
	if v, ok := get("gateway_peer"); ok {
		c.GatewayPeer = v
	}
	if v, ok := get("msp_id"); ok {
		c.MSPID = v
	}
	if v, ok := get("cert_path"); ok {
		c.CertPath = v
	}
	if v, ok := get("key_path"); ok {
		c.KeyPath = v
	}
	if v, ok := get("tls_ca_cert_path"); ok {
		c.TLSCACertPath = v
	}
	if v, ok := get("channel"); ok {
		c.Channel = v
	}
	if v, ok := get("chaincode"); ok {
		c.Chaincode = v
	}
	if v, ok := get("metrics_endpoint"); ok {
		c.MetricsEndpointURL = v
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

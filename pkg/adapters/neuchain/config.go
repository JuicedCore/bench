package neuchain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is the NeuChain adapter configuration, decoded from the run config's
// `adapter:` map. See docs/platforms/neuchain-client-implementation.md.
type Config struct {
	// BlockServers are host:port of each block server's SUBMIT (PUB target)
	// socket - NeuChain listens on :5001. Each transaction is published to ONE of
	// them, round-robin; the servers replicate it among themselves (deterministic
	// execution). Accepts a comma-separated string or a list.
	BlockServers []string `yaml:"block_servers"`

	// QueryEndpoint is host:port of one block server's QUERY (REQ/REP) socket -
	// NeuChain listens on :7003. Used for tip_query / block_query polling.
	QueryEndpoint string `yaml:"query_endpoint"`

	// UserPrivKeyPath / UserPubKeyPath are PEM PKCS#1 RSA key files produced by
	// NeuChain's `./user -b 1 1 1` crypto-init (in neuchain_release/bin/crypto/).
	// NeuChain generates them with an empty password.
	UserPrivKeyPath string `yaml:"user_priv_key_path"`
	UserPubKeyPath  string `yaml:"user_pub_key_path"` // informational; not read
	KeyPassword     string `yaml:"key_password"`      // NeuChain encrypts keys under an empty password

	// TableName goes into YCSB_PAYLOAD.table. The upstream YCSB chaincode
	// ignores it (it always uses table "ycsb"); it is sent for parity with
	// NeuChain's own client. The function name comes from the tx kind.
	TableName string `yaml:"table_name"`

	// PollInterval is how often the finality poller checks tip + new blocks.
	PollInterval time.Duration `yaml:"poll_interval"`

	// HeartbeatInterval is how often an "empty" liveness tx goes to each block
	// server (NeuChain's own `user` low-cost mode uses 100ms). "0s" disables it.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	heartbeatSet      bool

	// StartBlock is the block height to begin polling from. 0 (the default)
	// means the block after the tip at Setup: this run's transactions cannot be
	// in earlier blocks, and replaying history delays their observed finality.
	StartBlock uint64 `yaml:"start_block"`

	// SocketTimeout bounds every ZMQ send/recv, so a dead block server cannot
	// block a query (and with it the finality poller and Teardown) for zmq4's
	// 5 minute default. DialTimeout bounds each connection attempt.
	SocketTimeout time.Duration `yaml:"socket_timeout"`
	DialTimeout   time.Duration `yaml:"dial_timeout"`

	// MetricsEndpointURL is NeuChain's own /metrics, scraped once at end of run
	// for the native (never cross-platform) section. deploy/docker/monitoring/
	// prometheus.yml defines a neuchain job on :9743 for builds that expose it;
	// empty disables the scrape, which is the default since the upstream build
	// only exposes metrics when compiled for it.
	MetricsEndpointURL string `yaml:"metrics_endpoint"`
}

func (c *Config) applyDefaults() {
	if c.QueryEndpoint == "" && len(c.BlockServers) > 0 {
		host := c.BlockServers[0]
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		c.QueryEndpoint = host + ":7003"
	}
	if c.TableName == "" {
		c.TableName = "ycsb"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 50 * time.Millisecond
	}
	if !c.heartbeatSet {
		c.HeartbeatInterval = 100 * time.Millisecond
	}
	if c.SocketTimeout == 0 {
		c.SocketTimeout = 10 * time.Second
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
}

func (c *Config) validate() error {
	const hint = "is deploy/docker/neuchain/connection.env sourced?"
	var errs []error
	if len(c.BlockServers) == 0 {
		errs = append(errs, fmt.Errorf("neuchain: adapter.block_servers is required (host:5001 list; %s)", hint))
	}
	if c.QueryEndpoint == "" {
		errs = append(errs, fmt.Errorf("neuchain: adapter.query_endpoint is required (host:7003; %s)", hint))
	}
	if c.UserPrivKeyPath == "" {
		errs = append(errs, fmt.Errorf("neuchain: adapter.user_priv_key_path is required (%s)", hint))
	}
	for k, d := range map[string]time.Duration{"poll_interval": c.PollInterval, "socket_timeout": c.SocketTimeout, "dial_timeout": c.DialTimeout} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("neuchain: adapter.%s must be > 0, got %s", k, d))
		}
	}
	if c.HeartbeatInterval < 0 {
		errs = append(errs, fmt.Errorf("neuchain: adapter.heartbeat_interval must be >= 0 (0s disables), got %s", c.HeartbeatInterval))
	}
	return errors.Join(errs...)
}

func configFromExtra(extra map[string]any) (*Config, error) {
	c := &Config{}
	if extra != nil {
		switch v := extra["block_servers"].(type) {
		case string:
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					c.BlockServers = append(c.BlockServers, s)
				}
			}
		case []any:
			for i, e := range v {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("neuchain: adapter.block_servers[%d] = %v is not a host:port string", i, e)
				}
				c.BlockServers = append(c.BlockServers, s)
			}
		case nil:
		default:
			return nil, fmt.Errorf("neuchain: adapter.block_servers must be a comma-separated string or a list, got %T", v)
		}
		str := func(k string) string {
			s, _ := extra[k].(string)
			return s
		}
		c.QueryEndpoint = str("query_endpoint")
		c.MetricsEndpointURL = str("metrics_endpoint")
		c.UserPrivKeyPath = str("user_priv_key_path")
		c.UserPubKeyPath = str("user_pub_key_path")
		c.KeyPassword = str("key_password")
		if s := str("table_name"); s != "" {
			c.TableName = s
		}
		c.heartbeatSet = str("heartbeat_interval") != ""
		for key, dst := range map[string]*time.Duration{"poll_interval": &c.PollInterval, "heartbeat_interval": &c.HeartbeatInterval, "socket_timeout": &c.SocketTimeout, "dial_timeout": &c.DialTimeout} {
			if s := str(key); s != "" {
				d, err := time.ParseDuration(s)
				if err != nil {
					return nil, fmt.Errorf("neuchain: bad adapter.%s %q: %w (want e.g. 50ms)", key, s, err)
				}
				*dst = d
			}
		}
		switch sb := extra["start_block"].(type) {
		case int:
			if sb < 0 {
				return nil, fmt.Errorf("neuchain: adapter.start_block must not be negative, got %d", sb)
			}
			c.StartBlock = uint64(sb)
		case float64:
			c.StartBlock = uint64(sb)
		case string:
			if sb != "" {
				n, err := strconv.ParseUint(sb, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("neuchain: bad adapter.start_block %q: %w", sb, err)
				}
				c.StartBlock = n
			}
		}
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

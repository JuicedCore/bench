package neuchain

import (
	"fmt"
	"strings"
	"time"
)

// Config is the NeuChain adapter configuration, decoded from the run config's
// `adapter:` map. See docs/platforms/neuchain-client-implementation.md.
type Config struct {
	// BlockServers are host:port of each block server's SUBMIT (PUB target)
	// socket - NeuChain listens on :5001. Transactions are published to ALL of
	// them (deterministic execution). Accepts a comma-separated string or a list.
	BlockServers []string `yaml:"block_servers"`

	// QueryEndpoint is host:port of one block server's QUERY (REQ/REP) socket -
	// NeuChain listens on :7003. Used for tip_query / block_query polling.
	QueryEndpoint string `yaml:"query_endpoint"`

	// UserPrivKeyPath / UserPubKeyPath are PEM PKCS#1 RSA key files produced by
	// NeuChain's `./user -b 1 1 1` crypto-init (in neuchain_release/bin/crypto/).
	// NeuChain generates them with an empty password.
	UserPrivKeyPath string `yaml:"user_priv_key_path"`
	UserPubKeyPath  string `yaml:"user_pub_key_path"`
	KeyPassword     string `yaml:"key_password"`

	// FuncName / TableName go into TransactionPayload.header and
	// YCSB_PAYLOAD.table. They must match the deployed NeuChain chaincode config
	// (config-template.yaml: func_name / table_name).
	FuncName  string `yaml:"func_name"`
	TableName string `yaml:"table_name"`

	// PollInterval is how often the finality poller checks tip + new blocks.
	PollInterval time.Duration `yaml:"poll_interval"`

	// StartBlock is the block height to begin polling from (default 1).
	StartBlock uint64 `yaml:"start_block"`
}

func (c *Config) applyDefaults() {
	if c.QueryEndpoint == "" && len(c.BlockServers) > 0 {
		host := c.BlockServers[0]
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		c.QueryEndpoint = host + ":7003"
	}
	if c.FuncName == "" {
		c.FuncName = "ycsb"
	}
	if c.TableName == "" {
		c.TableName = "test_table"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 50 * time.Millisecond
	}
	if c.StartBlock == 0 {
		c.StartBlock = 1
	}
}

func (c *Config) validate() error {
	if len(c.BlockServers) == 0 {
		return fmt.Errorf("neuchain: block_servers is required (host:5001 list)")
	}
	if c.QueryEndpoint == "" {
		return fmt.Errorf("neuchain: query_endpoint is required (host:7003)")
	}
	if c.UserPrivKeyPath == "" {
		return fmt.Errorf("neuchain: user_priv_key_path is required")
	}
	return nil
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
			for _, e := range v {
				if s, ok := e.(string); ok {
					c.BlockServers = append(c.BlockServers, s)
				}
			}
		}
		str := func(k string) string {
			s, _ := extra[k].(string)
			return s
		}
		c.QueryEndpoint = str("query_endpoint")
		c.UserPrivKeyPath = str("user_priv_key_path")
		c.UserPubKeyPath = str("user_pub_key_path")
		c.KeyPassword = str("key_password")
		if s := str("func_name"); s != "" {
			c.FuncName = s
		}
		if s := str("table_name"); s != "" {
			c.TableName = s
		}
		if s := str("poll_interval"); s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				c.PollInterval = d
			} else {
				return nil, fmt.Errorf("neuchain: bad poll_interval %q: %w", s, err)
			}
		}
		switch sb := extra["start_block"].(type) {
		case int:
			c.StartBlock = uint64(sb)
		case float64:
			c.StartBlock = uint64(sb)
		}
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

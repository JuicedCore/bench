package fabricx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// fscClient is a thin HTTP client for the Fabric-X REST façade. It carries the
// ASSUMED request/response shapes; adjust here (and only here) once the real
// tokens-sample API is confirmed.
type fscClient struct {
	base string
	hc   *http.Client
	cfg  *Config
}

func newFSCClient(cfg *Config) *fscClient {
	return &fscClient{
		base: strings.TrimRight(cfg.BaseURL, "/"),
		hc:   &http.Client{Timeout: cfg.HTTPTimeout},
		cfg:  cfg,
	}
}

// ---- request / response bodies (assumed contract) ----

type kvReq struct {
	Op    string `json:"op"`              // "write" | "read"
	Key   string `json:"key"`
	Value string `json:"value,omitempty"` // base64, write only
}

type submitResp struct {
	TxID     string `json:"txID"`
	Accepted bool   `json:"accepted"`
	Value    string `json:"value,omitempty"` // base64, read responses
	Found    bool   `json:"found,omitempty"`
	Error    string `json:"error,omitempty"`
}

type transferReq struct {
	TokenType string `json:"tokenType"`
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    int64  `json:"amount"`
}

type statusResp struct {
	TxID     string `json:"txID"`
	Status   string `json:"status"` // "pending" | "committed" | "invalid"
	BlockNum uint64 `json:"blockNum"`
	Error    string `json:"error,omitempty"`
}

// ---- calls ----

func (c *fscClient) kvWrite(ctx context.Context, key string, value []byte) (*submitResp, error) {
	return c.postSubmit(ctx, c.cfg.KVRoute, kvReq{
		Op: "write", Key: key, Value: base64.StdEncoding.EncodeToString(value),
	})
}

func (c *fscClient) kvRead(ctx context.Context, key string) (*submitResp, error) {
	return c.postSubmit(ctx, c.cfg.KVRoute, kvReq{Op: "read", Key: key})
}

func (c *fscClient) transfer(ctx context.Context, from, to string, amount int64) (*submitResp, error) {
	return c.postSubmit(ctx, c.cfg.TransferRoute, transferReq{
		TokenType: c.cfg.TokenType, From: from, To: to, Amount: amount,
	})
}

func (c *fscClient) postSubmit(ctx context.Context, route string, body any) (*submitResp, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+route, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fabricx %s: HTTP %d: %s", route, resp.StatusCode, raw)
	}
	var sr submitResp
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("fabricx %s: bad response: %w", route, err)
	}
	if sr.Error != "" {
		return &sr, fmt.Errorf("fabricx %s: %s", route, sr.Error)
	}
	return &sr, nil
}

// status does one commit-status GET.
func (c *fscClient) status(ctx context.Context, txID string) (*statusResp, error) {
	u := c.base + fmt.Sprintf(c.cfg.TxStatusRoute, txID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fabricx status %s: HTTP %d: %s", txID, resp.StatusCode, raw)
	}
	var s statusResp
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// waitFinality blocks until the tx is committed/invalid or the deadline passes,
// using either long-poll or a status poll loop per Config.FinalityMode.
func (c *fscClient) waitFinality(ctx context.Context, txID string, timeout time.Duration) (*statusResp, error) {
	deadline := time.Now().Add(timeout)
	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	if c.cfg.FinalityMode == "longpoll" {
		u := c.base + fmt.Sprintf(c.cfg.TxWaitRoute, txID)
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("fabricx wait %s: HTTP %d: %s", txID, resp.StatusCode, raw)
		}
		var s statusResp
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return &s, nil
	}

	t := time.NewTicker(c.cfg.PollInterval)
	defer t.Stop()
	for {
		s, err := c.status(cctx, txID)
		if err == nil && s.Status != "pending" && s.Status != "" {
			return s, nil
		}
		select {
		case <-cctx.Done():
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("fabricx: finality timeout for %s", txID)
		case <-t.C:
		}
	}
}

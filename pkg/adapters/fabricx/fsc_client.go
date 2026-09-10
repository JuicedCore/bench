package fabricx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// fscClient is a thin HTTP client for the fabric-x-samples tokens REST services
// plus the custom kv-write view. Request/response shapes below are transcribed
// from hyperledger/fabric-x-samples/tokens/swagger.yaml.
type fscClient struct {
	hc  *http.Client
	cfg *Config
}

func newFSCClient(cfg *Config) *fscClient {
	return &fscClient{hc: &http.Client{Timeout: cfg.HTTPTimeout}, cfg: cfg}
}

// ---- token API bodies (swagger.yaml) ----

type amount struct {
	Code  string `json:"code"`
	Value uint64 `json:"value"`
}

type counterparty struct {
	Node    string `json:"node"`
	Account string `json:"account"`
}

// transferRequest is the swagger "TransferRequest" (used for both transfer and issue).
type transferRequest struct {
	Amount       amount       `json:"amount"`
	Counterparty counterparty `json:"counterparty"`
	Message      string       `json:"message,omitempty"`
}

// tokenResponse is the swagger "TransferSuccess"/"IssueSuccess" shape:
// { "message": "...", "payload": "<txid>" }.
type tokenResponse struct {
	Message string `json:"message"`
	Payload string `json:"payload"`
}

// account is the swagger "Account" shape returned by GET /owner/accounts/{id}.
type account struct {
	ID      string   `json:"id"`
	Balance []amount `json:"balance"`
}

type accountResponse struct {
	Message string  `json:"message"`
	Payload account `json:"payload"`
}

// ---- custom kv-write view body (deploy/docker/fabricx/kvview) ----

type kvRequest struct {
	Op    string `json:"op"`              // "write" | "read"
	Key   string `json:"key"`
	Value string `json:"value,omitempty"` // base64, write only
}

type kvResponse struct {
	TxID  string `json:"txID"`
	Value string `json:"value,omitempty"` // base64, read replies
	Found bool   `json:"found,omitempty"`
	Error string `json:"error,omitempty"`
}

// ---- calls ----

// transfer POSTs to the owner service. Returns the tx id. This call blocks until
// the transaction is ordered AND final (fabric-x-samples runs
// ttx.NewOrderingAndFinalityView before responding) - so its return marks T3.
func (c *fscClient) transfer(ctx context.Context, sender, recipient string, value uint64) (string, error) {
	body := transferRequest{
		Amount:       amount{Code: c.cfg.TokenCode, Value: value},
		Counterparty: counterparty{Node: c.cfg.CounterpartyNode, Account: recipient},
	}
	u := strings.TrimRight(c.cfg.OwnerURL, "/") + "/owner/accounts/" + url.PathEscape(sender) + "/transfer"
	var out tokenResponse
	if err := c.postJSON(ctx, u, body, &out); err != nil {
		return "", err
	}
	return out.Payload, nil
}

// issue POSTs to the issuer service (mint to an account). Also synchronous to finality.
func (c *fscClient) issue(ctx context.Context, recipient string, value uint64) (string, error) {
	body := transferRequest{
		Amount:       amount{Code: c.cfg.TokenCode, Value: value},
		Counterparty: counterparty{Node: c.cfg.CounterpartyNode, Account: recipient},
	}
	u := strings.TrimRight(c.cfg.IssuerURL, "/") + "/issuer/issue"
	var out tokenResponse
	if err := c.postJSON(ctx, u, body, &out); err != nil {
		return "", err
	}
	return out.Payload, nil
}

// kvWrite / kvRead hit the custom FSC view service. kvWrite is synchronous to
// finality (the view runs ordering+finality); its return marks T3.
func (c *fscClient) kvWrite(ctx context.Context, key string, value []byte) (*kvResponse, error) {
	return c.kv(ctx, kvRequest{Op: "write", Key: key, Value: base64.StdEncoding.EncodeToString(value)})
}

func (c *fscClient) kvRead(ctx context.Context, key string) (*kvResponse, error) {
	return c.kv(ctx, kvRequest{Op: "read", Key: key})
}

func (c *fscClient) kv(ctx context.Context, req kvRequest) (*kvResponse, error) {
	u := strings.TrimRight(c.cfg.KVURL, "/") + "/kv"
	var out kvResponse
	if err := c.postJSON(ctx, u, req, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return &out, fmt.Errorf("fabricx kv: %s", out.Error)
	}
	return &out, nil
}

// balance reads an account's balance for the configured token code.
func (c *fscClient) balance(ctx context.Context, accountID string) (*account, error) {
	u := strings.TrimRight(c.cfg.OwnerURL, "/") + "/owner/accounts/" + url.PathEscape(accountID) +
		"?code=" + url.QueryEscape(c.cfg.TokenCode)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fabricx balance %s: HTTP %d: %s", accountID, resp.StatusCode, raw)
	}
	var ar accountResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, err
	}
	return &ar.Payload, nil
}

// health probes GET {OwnerURL}/healthz for the reachability check.
func (c *fscClient) health(ctx context.Context) error {
	base := c.cfg.OwnerURL
	if base == "" {
		base = c.cfg.KVURL
	}
	u := strings.TrimRight(base, "/") + "/healthz"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *fscClient) postJSON(ctx context.Context, u string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fabricx POST %s: HTTP %d: %s", u, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("fabricx POST %s: bad response: %w", u, err)
		}
	}
	return nil
}

// Package control implements the versioned panel HTTP protocol for agents.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxypanel/internal/contracts"
)

// ErrNoChange is returned for HTTP 204 responses; 204 never carries a body.
var ErrNoChange = errors.New("no change")

// APIError is a non-2xx panel response or a 2xx response without acceptance.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("panel returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("panel returned HTTP %d: %s: %s", e.Status, e.Code, e.Message)
}

// Unauthorized reports credential problems that need operator action.
func (e *APIError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Retryable reports whether another attempt can succeed.
func (e *APIError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout
}

// Client speaks the agent API. The bearer token is never logged.
type Client struct {
	base    *url.URL
	http    *http.Client
	version string
	maxBody int64

	mu      sync.RWMutex
	token   string
	agentID string
}

// Endpoint paths.
const (
	PathRegister  = "/api/v1/agent/register"
	PathSnapshot  = "/api/v1/agent/snapshot"
	PathApplied   = "/api/v1/agent/applied"
	PathUsage     = "/api/v1/agent/usage"
	PathHeartbeat = "/api/v1/agent/heartbeat"

	defaultMaxBody = 16 << 20
)

// New builds a client for the given panel base URL.
func New(baseURL, version string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("panel URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("panel URL %q is not absolute", baseURL)
	}
	if version == "" {
		version = "unknown"
	}
	return &Client{
		base:    u,
		version: version,
		maxBody: defaultMaxBody,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:                 nil, // never route agent traffic through a proxy env
				MaxIdleConns:          4,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
	}, nil
}

// SetHTTPClient replaces the HTTP client (tests).
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.http = hc
	}
}

// SetToken stores the agent bearer token.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// SetAgentID stores the panel assigned agent id.
func (c *Client) SetAgentID(id string) {
	c.mu.Lock()
	c.agentID = id
	c.mu.Unlock()
}

// AgentID returns the panel assigned agent id.
func (c *Client) AgentID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agentID
}

// Token returns the bearer token. It is only used to redact core output.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// HasToken reports whether a bearer token is configured.
func (c *Client) HasToken() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token != ""
}

// BaseURL returns the panel base URL.
func (c *Client) BaseURL() string { return c.base.String() }

// Register exchanges a one-time registration code for an agent identity.
func (c *Client) Register(ctx context.Context, req contracts.RegistrationRequest) (contracts.RegistrationResponse, error) {
	var resp contracts.RegistrationResponse
	err := c.do(ctx, http.MethodPost, PathRegister, nil, req, &resp, false)
	return resp, err
}

// Snapshot fetches the authorized snapshot. It returns (nil, nil) for 204, which
// means "the revision you have is still current".
//
// The request carries the currently applied revision and, when known, the
// control generation. The extra generation parameter is optional for the panel:
// it exists so a panel that changed (or restored) its control generation can
// never answer with 204, which would silently keep an authorization the agent
// cannot verify.
func (c *Client) Snapshot(ctx context.Context, revision int64, generation string) (*contracts.Snapshot, error) {
	var query url.Values
	if revision > 0 {
		query = url.Values{"revision": []string{strconv.FormatInt(revision, 10)}}
	}
	if generation = strings.TrimSpace(generation); generation != "" {
		if query == nil {
			query = url.Values{}
		}
		query.Set("control_generation", generation)
	}
	var snap contracts.Snapshot
	err := c.do(ctx, http.MethodGet, PathSnapshot, query, nil, &snap, true)
	if errors.Is(err, ErrNoChange) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// Applied reports the outcome of one snapshot application.
func (c *Client) Applied(ctx context.Context, report contracts.ApplyReport) error {
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.do(ctx, http.MethodPost, PathApplied, nil, report, &ack, false); err != nil {
		return err
	}
	if !ack.Accepted {
		return &APIError{Status: http.StatusOK, Code: "NOT_ACCEPTED", Message: "panel did not accept the apply report"}
	}
	return nil
}

// Usage uploads one usage batch.
func (c *Client) Usage(ctx context.Context, batch contracts.UsageBatch) (contracts.UsageAck, error) {
	var ack contracts.UsageAck
	err := c.do(ctx, http.MethodPost, PathUsage, nil, batch, &ack, false)
	return ack, err
}

// Heartbeat sends the periodic heartbeat.
func (c *Client) Heartbeat(ctx context.Context, hb contracts.Heartbeat) error {
	var ack struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.do(ctx, http.MethodPost, PathHeartbeat, nil, hb, &ack, false); err != nil {
		return err
	}
	if !ack.Accepted {
		return &APIError{Status: http.StatusOK, Code: "NOT_ACCEPTED", Message: "panel did not accept the heartbeat"}
	}
	return nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, allowNoContent bool) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "proxypanel-agent/"+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusNoContent {
		if !allowNoContent {
			return &APIError{Status: resp.StatusCode, Code: "UNEXPECTED_204", Message: "panel returned 204 for an endpoint that requires a body"}
		}
		return ErrNoChange
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		var env struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			apiErr.Code = env.Error.Code
			apiErr.Message = truncate(env.Error.Message, 300)
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("%s response is missing the data envelope", path)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode %s data: %w", path, err)
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// Backoff is a bounded exponential backoff with jitter.
type Backoff struct {
	Base     time.Duration
	Max      time.Duration
	attempt  int
	jitterFn func(time.Duration) time.Duration
}

// NewBackoff builds a backoff starting at base and capped at max.
func NewBackoff(base, max time.Duration) *Backoff {
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 || max < base {
		max = 60 * time.Second
	}
	return &Backoff{Base: base, Max: max, jitterFn: jitter}
}

// Reset clears the attempt counter.
func (b *Backoff) Reset() { b.attempt = 0 }

// Next returns the next delay and advances the attempt counter.
func (b *Backoff) Next() time.Duration {
	d := b.Base
	for i := 0; i < b.attempt; i++ {
		d *= 2
		if d >= b.Max {
			d = b.Max
			break
		}
	}
	b.attempt++
	if b.jitterFn != nil {
		d = b.jitterFn(d)
	}
	if d > b.Max {
		d = b.Max
	}
	return d
}

// Attempts returns how many delays were produced so far.
func (b *Backoff) Attempts() int { return b.attempt }

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := int64(d) / 5
	if delta <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(2*delta)-delta) //nolint:gosec // jitter only
}

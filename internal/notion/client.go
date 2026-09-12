// Package notion implements a client for Notion's private /api/v3 endpoints,
// used by the Notion AI web app. Everything upstream-specific lives here:
// if Notion changes payload or stream formats, this package is the only place
// that needs updating.
//
// The API is undocumented and reverse-engineered from the Notion web client.
// See DISCLAIMER.md before using this software.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
)

// Upstream error classes consumed by the pool for failover decisions.
var (
	ErrAuth        = errors.New("notion: auth failed (token invalid or expired)")
	ErrRateLimited = errors.New("notion: rate limited")
	ErrUpstream    = errors.New("notion: upstream error")
)

// RateLimitError carries the upstream Retry-After hint and unwraps to
// ErrRateLimited so errors.Is works for pool failover classification.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("notion: rate limited (retry after %s)", e.RetryAfter)
	}
	return "notion: rate limited"
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Client talks to Notion on behalf of exactly one account.
type Client struct {
	base      string
	token     string // token_v2 cookie value
	userID    string
	spaceID   string
	http      *http.Client
	clientVer string
	userAgent string
	timeout   time.Duration // per upstream call; 0 = no limit
}

// NewClient builds a client for one account. proxyURL may be empty.
func NewClient(cfg *config.Config, acc model.Account) (*Client, error) {
	// Fail-closed: an unparseable egress proxy must NOT silently fall back to
	// direct connections (token_v2 would leak from the real IP).
	if err := validateProxy(acc.Proxy); err != nil {
		return nil, err
	}
	transport := newTransport(acc.Proxy)
	base := cfg.NotionBaseURL
	if acc.BaseURL != "" {
		base = acc.BaseURL
	}
	// Early fail on non-HTTP(S) schemes (file:// etc.) — config errors must
	// surface at startup, not on every request.
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid Notion base URL %q (want http/https)", base)
	}
	return &Client{
		base:      base,
		token:     acc.TokenV2,
		userID:    acc.UserID,
		spaceID:   acc.SpaceID,
		clientVer: cfg.NotionClientVersion,
		userAgent: cfg.UserAgent,
		timeout:   cfg.UpstreamTimeout,
		http: &http.Client{
			Transport: transport,
			Timeout:   0, // streaming: control via request context
		},
	}, nil
}

// flipDomain returns the alternate Notion base URL (notion.so ↔ notion.com).
// Notion migrates accounts between the two domains; the bootstrap probes both
// and the working one is persisted on the account.
func flipDomain(base string) string {
	switch {
	case strings.Contains(base, "notion.so"):
		return strings.Replace(base, "notion.so", "notion.com", 1)
	case strings.Contains(base, "notion.com"):
		return strings.Replace(base, "notion.com", "notion.so", 1)
	default:
		return ""
	}
}

// validateProxy rejects proxy URLs we cannot parse; scheme must be known.
func validateProxy(proxyURL string) error {
	if proxyURL == "" {
		return nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid egress proxy URL %q (fail-closed)", proxyURL)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("unsupported egress proxy scheme %q", u.Scheme)
	}
}

func newTransport(proxyURL string) *http.Transport {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        4,
		MaxConnsPerHost:     4, // never open a burst of parallel upstream conns
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			t.Proxy = http.ProxyURL(u)
		}
	}
	return t
}

// maxRetryAfter caps upstream Retry-After hints so a hostile value cannot
// knock an account out of rotation for good.
const maxRetryAfter = 10 * time.Minute

// apiURL builds an absolute /api/v3 endpoint URL.
func (c *Client) apiURL(endpoint string) string { return c.base + "/api/v3/" + endpoint }

// do performs an authenticated POST/GET against /api/v3.
func (c *Client) do(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL(endpoint), rdr)
	if err != nil {
		return nil, fmt.Errorf("notion: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson, application/json")
	if c.token != "" {
		req.Header.Set("Cookie", "token_v2="+c.token)
	}
	if c.userID != "" {
		req.Header.Set("notion-user-id", c.userID)
	}
	if c.clientVer != "" {
		req.Header.Set("notion-client-version", c.clientVer)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notion: request %s: %w", endpoint, err)
	}
	return resp, nil
}

// classify maps an upstream HTTP status onto pool-relevant error classes.
//
// ВАЖНО: Notion может вернуть 400/403 с телом aiNotEnabled для space без AI.
// Такое тело проверяется вызывающим кодом через classifyWithBody; голый
// classify без тела используется только когда тела уже нет (drain).
func classify(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusTooManyRequests:
		ra := resp.Header.Get("Retry-After")
		var d time.Duration
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
			if d > maxRetryAfter {
				d = maxRetryAfter // guard against absurd/overflowing values
			}
		}
		return &RateLimitError{RetryAfter: d}
	default:
		if resp.StatusCode >= 400 {
			return fmt.Errorf("%w: HTTP %d", ErrUpstream, resp.StatusCode)
		}
		return nil
	}
}

// doInference performs the runInferenceTranscript request with the full
// browser-parity header set of the current Notion web app.
func (c *Client) doInference(ctx context.Context, body []byte, spaceID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("runInferenceTranscript"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("notion: build request: %w", err)
	}
	cookie := "token_v2=" + c.token
	if c.userID != "" {
		cookie += "; notion_user_id=" + c.userID
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("x-notion-space-id", spaceID)
	if c.userID != "" {
		req.Header.Set("x-notion-active-user-header", c.userID)
	}
	req.Header.Set("notion-audit-log-platform", "web")
	if c.clientVer != "" {
		req.Header.Set("notion-client-version", c.clientVer)
	}
	req.Header.Set("origin", c.base)
	req.Header.Set("referer", c.base+"/ai")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notion: request runInferenceTranscript: %w", err)
	}
	return resp, nil
}

// classifyWithBody is classify + sniffing тела ошибки: 400/403 с
// aiNotEnabled внутри — это ErrAINotEnabled (скип space), а не ErrAuth
// (terminal invalid). Без этого space без AI убивал аккаунт навсегда.
func classifyWithBody(resp *http.Response, body []byte) error {
	if resp.StatusCode >= 400 && isAINotEnabledMessage(string(body)) {
		return fmt.Errorf("%w: %s", ErrAINotEnabled, firstBytes(body, 300))
	}
	if resp.StatusCode >= 400 && isModelDisabledMessage(string(body)) {
		return fmt.Errorf("%w: %s", ErrModelDisabled, firstBytes(body, 300))
	}
	return classify(resp)
}

func firstBytes(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…(truncated)"
	}
	return s
}

// postJSON performs a POST and decodes the JSON response.
func (c *Client) postJSON(ctx context.Context, endpoint string, payload []byte) (json.RawMessage, error) {
	resp, err := c.do(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("notion: read %s: %w", endpoint, err)
	}
	if err := classifyWithBody(resp, body); err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("notion: empty response from %s", endpoint)
	}
	return json.RawMessage(body), nil
}

// Ping performs a cheap authenticated call to verify the session is alive.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, "getSpaces", []byte("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	drain(resp.Body)
	if err := classify(resp); err != nil {
		return err
	}
	return nil
}

func drain(r io.Reader) {
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 1<<20))
}

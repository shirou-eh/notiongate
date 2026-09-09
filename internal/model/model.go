// Package model defines the core domain types shared across notiongate.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"
)

// Account lifecycle statuses.
const (
	StatusActive    = "active"    // in rotation, usage below rotate threshold
	StatusReserve   = "reserve"   // usage >= rotate threshold, used only as fallback
	StatusExhausted = "exhausted" // usage >= 100% of window limit
	StatusCooldown  = "cooldown"  // temporary backoff (e.g. 429 from upstream)
	StatusInvalid   = "invalid"   // token rejected (401/403) — needs re-login
	StatusDisabled  = "disabled"  // manually disabled by admin
)

// Window types for usage counters.
const (
	WindowDay   = "day"
	WindowMonth = "month"
)

// Account is a Notion session usable for inference.
type Account struct {
	ID        string   `json:"id"`
	Label     string   `json:"label,omitempty"`
	TokenV2   string   `json:"-"` // never serialized directly
	UserID      string `json:"user_id,omitempty"`
	SpaceID     string `json:"space_id,omitempty"`
	SpaceViewID string `json:"space_view_id,omitempty"`
	SpaceName string   `json:"space_name,omitempty"`
	Email     string   `json:"email,omitempty"`
	Status    string   `json:"status"`
	Models    []string `json:"models,omitempty"`

	// Per-account quota policy.
	LimitReq   int64   `json:"limit_req"`          // requests per window; 0 = unlimited
	WindowType string  `json:"window_type"`        // day | month
	RotateAt   float64 `json:"rotate_at"`          // e.g. 0.8 → switch at 80%
	Proxy      string  `json:"proxy,omitempty"`    // egress proxy (http/socks5)
	BaseURL    string  `json:"base_url,omitempty"` // discovered upstream base (notion.so/.com)

	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastUsed      time.Time `json:"last_used,omitempty"`
	LastCheck     time.Time `json:"last_check,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// MaskedProxy returns a display-safe version of the egress proxy URL
// (credentials stripped).
func (a *Account) MaskedProxy() string {
	if a.Proxy == "" {
		return ""
	}
	u, err := url.Parse(a.Proxy)
	if err != nil {
		return "[unparseable]"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

// ViewProxy is the JSON-safe representation used in admin responses.
func (a *Account) ViewProxy() string { return a.MaskedProxy() }

// MaskedToken returns a display-safe version of the cookie.
func (a *Account) MaskedToken() string {
	if a.TokenV2 == "" {
		return ""
	}
	n := len(a.TokenV2)
	if n <= 12 {
		return "***"
	}
	return a.TokenV2[:8] + "..." + a.TokenV2[n-4:]
}

// EffectiveLimit returns the effective request limit for this account.
func (a *Account) EffectiveLimit(def int64) int64 {
	if a.LimitReq > 0 {
		return a.LimitReq
	}
	return def
}

// EffectiveRotate returns the effective rotation threshold.
func (a *Account) EffectiveRotate(def float64) float64 {
	if a.RotateAt > 0 && a.RotateAt <= 1 {
		return a.RotateAt
	}
	return def
}

// Counter is the usage of one account within one window.
type Counter struct {
	AccountID string `json:"account_id"`
	WindowKey string `json:"window_key"`
	ReqCount  int64  `json:"req_count"`
	InTokens  int64  `json:"in_tokens"`
	OutTokens int64  `json:"out_tokens"`
}

// RequestLog is one completed (or failed) inference request, for stats.
type RequestLog struct {
	ID        string    `json:"id"`
	TS        time.Time `json:"ts"`
	AccountID string    `json:"account_id"`
	Model     string    `json:"model"`
	Protocol  string    `json:"protocol"` // openai | anthropic
	InTokens  int64     `json:"in_tokens"`
	OutTokens int64     `json:"out_tokens"`
	LatencyMS int64     `json:"latency_ms"`
	Status    string    `json:"status"` // ok | error
	ErrCode   string    `json:"err_code,omitempty"`
}

// WindowKey renders the usage window bucket for a time instant.
func WindowKey(t time.Time, windowType string) string {
	switch windowType {
	case WindowDay:
		return t.UTC().Format("day:2006-01-02")
	case WindowMonth:
		return t.UTC().Format("month:2006-01")
	default:
		return t.UTC().Format("month:2006-01")
	}
}

// NewID returns a random RFC-4122-like UUID v4 string.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err)) //nolint:forbidigo // unreachable in practice
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := make([]byte, 36)
	hex.Encode(h[0:8], b[0:4])
	h[8] = '-'
	hex.Encode(h[9:13], b[4:6])
	h[13] = '-'
	hex.Encode(h[14:18], b[6:8])
	h[18] = '-'
	hex.Encode(h[19:23], b[8:10])
	h[23] = '-'
	hex.Encode(h[24:36], b[10:16])
	return string(h)
}

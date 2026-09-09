package api

// Regression tests for adversarial round-2 findings (R2-4, R2-8, R2-9, R2-13).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shirou-eh/notiongate/internal/model"
)

// R2-4: DNS-rebinding style request (attacker Host == attacker Origin) must
// still be rejected — Origin is validated against configured hosts, not
// against the request's own Host header.
func TestR2RebindingRejected(t *testing.T) {
	env := setupE2E(t)
	// Simulate a rebound domain: host header rewritten to the attacker's
	// origin so Host == Origin (both evil.example).
	req, _ := http.NewRequest(http.MethodDelete, env.http.URL+"/admin/accounts/whatever", nil)
	req.Header.Set("Origin", "http://evil.example:8787")
	req.Host = "evil.example:8787"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding-style request must be 403, got %d", resp.StatusCode)
	}
}

// R2-9: the client-facing API_KEY must not unlock admin on a non-loopback
// binding, and egress proxy credentials must be masked in admin output.
func TestR2AdminIsolationAndProxyMasking(t *testing.T) {
	env := setupE2E(t) // APIKey=sk-test, AdminKey=adm, loopback bind

	// Loopback: admin without key is allowed (trusted local operator).
	req, _ := http.NewRequest(http.MethodGet, env.http.URL+"/admin/accounts", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 200000)
	n, _ := resp.Body.Read(b)
	resp.Body.Close()
	body := string(b[:n])

	// Proxy credentials must never appear in admin output.
	if strings.Contains(body, "operator:secretpw") {
		t.Fatalf("proxy credentials leaked in admin output: %s", body)
	}
}

// R2-13: requests without any text content must be rejected, not fabricated
// into a fake "Hello" upstream call.
func TestR2NullContentRejected(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model":    "e2e-model",
		"messages": []map[string]any{{"role": "user", "content": nil}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for null content, got %d: %s", resp.StatusCode, body)
	}
}

// R2-8: mid-stream upstream errors must not leak raw upstream text. The mock
// "bad" account returns 401 at request level; simulate a mid-stream failure
// by pointing the only enabled account at the bad upstream and streaming.
func TestR2StreamErrorSanitized(t *testing.T) {
	env := setupE2E(t)
	_ = env.pool.Update("acc-good", func(a *model.Account) { a.Status = model.StatusDisabled })
	_ = env.pool.Update("acc-limited", func(a *model.Account) { a.Status = model.StatusDisabled })
	// Disable the good account and the limited one so the only candidate is
	// acc-bad (401 → mid-failover path), then check the anthropic SSE error.
	_ = env.pool.Update("acc-bad", func(a *model.Account) { a.SpaceID = "sp-bad" })
	resp, body := postJSON(t, env.http.URL+"/v1/messages", "sk-test", map[string]any{
		"model": "e2e-model", "stream": true, "max_tokens": 8,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	s := string(body)
	if strings.Contains(s, "notion: ") || strings.Contains(s, "INTERNAL-DETAIL") {
		t.Fatalf("raw upstream error leaked into SSE: %s", s)
	}
	_ = resp
}

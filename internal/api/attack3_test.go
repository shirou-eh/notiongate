package api

// Regression tests for adversarial round-3 findings (R3-5, R3-8).

import (
	"net/http"
	"strings"
	"testing"
)

// R3-5: /healthz must stay cheap and must not fingerprint the pool.
func TestR3HealthzCheapNoDetails(t *testing.T) {
	env := setupE2E(t)
	resp, err := http.Get(env.http.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	body := string(b[:n])
	if strings.Contains(body, `"pool"`) || strings.Contains(body, `"by_status"`) {
		t.Fatalf("healthz leaks pool details: %s", body)
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("healthz broken: %s", body)
	}
}

// R3-8: whitespace-only messages must be rejected before touching upstream.
func TestR3WhitespaceOnlyRejected(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model":    "e2e-model",
		"messages": []map[string]any{{"role": "user", "content": "\n\n \t"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only content, got %d: %s", resp.StatusCode, body)
	}
	// And the account must not have been used.
	c, _ := env.st.GetCounter("acc-good", "month:2026-09")
	if c.ReqCount != 0 {
		t.Fatalf("whitespace request burned quota: %d", c.ReqCount)
	}
}

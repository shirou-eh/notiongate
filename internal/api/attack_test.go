package api

// Regression tests for adversarial findings A-4, A-10, A-14 (round 1).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shirou-eh/notiongate/internal/model"
)

// A-4: cross-origin (CSRF-style) mutations must be rejected even in
// no-auth loopback mode.
func TestAttackCrossOriginRejected(t *testing.T) {
	env := setupE2E(t)

	mk := func(origin, ctype string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, env.http.URL+"/admin/accounts",
			strings.NewReader(`{"token_v2":"tok-evil","force":true}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 300)
		n, _ := resp.Body.Read(b)
		return resp, string(b[:n])
	}

	// Evil page firing a simple cross-origin request.
	resp, _ := mk("https://evil.example", "text/plain")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST must be 403, got %d", resp.StatusCode)
	}

	// Form-encoded variant too.
	resp2, _ := mk("https://evil.example", "application/x-www-form-urlencoded")
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin form POST must be 403, got %d", resp2.StatusCode)
	}

	// Same-origin and origin-less requests still work.
	resp3, _ := mk("", "application/json")
	if resp3.StatusCode == http.StatusForbidden {
		t.Fatal("same-origin request must not be rejected as cross-origin")
	}
}

// A-4b: same check on the chat endpoint (quota burn prevention).
func TestAttackCrossOriginChatRejected(t *testing.T) {
	env := setupE2E(t)
	req, _ := http.NewRequest(http.MethodPost, env.http.URL+"/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin chat must be 403, got %d", resp.StatusCode)
	}
}

// A-14: raw upstream internals must not be reflected to the client.
func TestAttackUpstreamErrorSanitized(t *testing.T) {
	env := setupE2E(t)
	_ = env.pool.Update("acc-good", func(a *model.Account) { a.Status = model.StatusDisabled })
	_ = env.pool.Update("acc-limited", func(a *model.Account) { a.Status = model.StatusDisabled })

	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model":    "e2e-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		s := string(body)
		if strings.Contains(s, "runInferenceTranscript") || strings.Contains(s, "notion: ") {
			t.Fatalf("internal error details leaked to client: %s", s)
		}
	}
}

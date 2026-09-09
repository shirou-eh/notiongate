package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/store"
)

// newMockNotion emulates Notion's private API for three account kinds:
// tokbad (401), toklimited (429) and everything else (NDJSON stream).
func newMockNotion(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	tok := func(r *http.Request) string {
		c := r.Header.Get("Cookie")
		if strings.Contains(c, "token_v2=tokbad") {
			return "bad"
		}
		if strings.Contains(c, "token_v2=toklimited") {
			return "limited"
		}
		return "good"
	}
	uid := func(r *http.Request) string { return "u-" + tok(r) }

	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		switch tok(r) {
		case "bad":
			w.WriteHeader(http.StatusUnauthorized)
			return
		case "limited":
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// nested 2026 format
		fmt.Fprintf(w, `{"%s":{"notion_user":{"%s":{"value":{"value":{"email":"%s@test.io"}}}},"space":{"sp-%s":{"spaceId":"sp-%s","value":{"value":{"id":"sp-%s","name":"WS","plan_type":"team","settings":{"enable_ai_feature":true}}}}},"space_view":{"sv-%s":{"spaceId":"sp-%s"}}}}`,
			uid(r), uid(r), uid(r), tok(r), tok(r), tok(r), tok(r), tok(r))
	})
	mux.HandleFunc("POST /api/v3/loadUserContent", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"recordMap":{"notion_user":{"%s":{"value":{"email":"%s@test.io"}}}}}`, uid(r), uid(r))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"models":[{"id":"e2e-model"},{"id":"e2e-mini"}]}`)
	})
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		switch tok(r) {
		case "bad":
			w.WriteHeader(http.StatusUnauthorized)
			return
		case "limited":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		lines := []string{
			`{"type":"patch-start","data":{"s":[{"id":"cfg","type":"config"},{"id":"ctx","type":"context"}]}}`,
			`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"m1","type":"agent-inference","value":[{"type":"thinking","content":"deep"},{"type":"text","content":"Hel"}]}}]}`,
			`{"type":"patch","v":[{"o":"a","p":"/s/2/value/-","v":{"type":"text","content":"lo"}}]}`,
			`{"type":"patch","v":[{"o":"x","p":"/s/2/value/1/content","v":" world"}]}`,
			`{"type":"patch","v":[{"o":"a","p":"/s/2/inputTokens","v":11},{"o":"a","p":"/s/2/outputTokens","v":7}]}`,
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
	return httptest.NewServer(mux)
}

type e2eEnv struct {
	notion *httptest.Server
	http   *httptest.Server
	pool   *pool.Pool
	st     *store.Store
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	mock := newMockNotion(t)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: true, MaxAttempts: 5,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	accs := []model.Account{
		{ID: "acc-bad", Label: "bad", TokenV2: "tokbad", UserID: "u-bad", SpaceID: "sp-bad",
			Status: model.StatusActive, Models: []string{"e2e-model"}, LastUsed: now.Add(-2 * time.Hour)},
		{ID: "acc-limited", Label: "limited", TokenV2: "toklimited", UserID: "u-limited", SpaceID: "sp-limited",
			Status: model.StatusActive, Models: []string{"e2e-model"}, LastUsed: now.Add(-time.Hour)},
		{ID: "acc-good", Label: "good", TokenV2: "tokgood", UserID: "u-good", SpaceID: "sp-good",
			Status: model.StatusActive, Models: []string{"e2e-model", "e2e-mini"}, LastUsed: now},
	}
	for _, a := range accs {
		if err := p.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)
	return &e2eEnv{notion: mock, http: ts, pool: p, st: st}
}

func postJSON(t *testing.T, url, key string, body any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestE2EOpenAINonStreamWithFailover(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "hi"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("unexpected response: %s", body)
	}
	if out.Choices[0].Message.Role != "assistant" || out.Usage.CompletionTokens == 0 {
		t.Fatalf("unexpected usage/role: %+v", out)
	}

	// Failover bookkeeping: bad → invalid, limited → cooldown, good → active.
	views := map[string]AccountViewLike{}
	for _, v := range env.pool.Snapshot() {
		views[v.ID] = AccountViewLike{Status: v.EffectiveStatus}
	}
	if views["acc-bad"].Status != model.StatusInvalid {
		t.Fatalf("bad = %v", views["acc-bad"])
	}
	if views["acc-limited"].Status != model.StatusCooldown {
		t.Fatalf("limited = %v", views["acc-limited"])
	}
	if views["acc-good"].Status != model.StatusActive {
		t.Fatalf("good = %v", views["acc-good"])
	}
}

// AccountViewLike avoids importing pool internals in assertions.
type AccountViewLike struct{ Status string }

func TestE2EOpenAIStream(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}
	var content strings.Builder
	sawRole, sawDone := false, false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &ch) == nil && len(ch.Choices) > 0 {
			if ch.Choices[0].Delta.Role != "" {
				sawRole = true
			}
			content.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if !sawRole || !sawDone {
		t.Fatalf("role chunk=%v done=%v body=%s", sawRole, sawDone, body)
	}
	if content.String() != "Hello world" {
		t.Fatalf("streamed content = %q", content.String())
	}
}

func TestE2EAnthropicStream(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/messages", "sk-test", map[string]any{
		"model": "e2e-model", "stream": true, "max_tokens": 64,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	text := strings.Builder{}
	saw := map[string]bool{}
	var curType string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "event: "):
			saw[strings.TrimPrefix(line, "event: ")] = true
		case strings.HasPrefix(line, "data: "):
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
				ContentBlock *struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
				continue
			}
			if ev.Type == "content_block_start" && ev.ContentBlock != nil {
				curType = ev.ContentBlock.Type
			}
			if ev.Type == "content_block_delta" {
				switch ev.Delta.Type {
				case "text_delta":
					if curType != "text" {
						t.Fatalf("text_delta in %s block", curType)
					}
					text.WriteString(ev.Delta.Text)
				case "thinking_delta":
					if curType != "thinking" {
						t.Fatalf("thinking_delta in %s block", curType)
					}
				}
			}
		}
	}
	for _, want := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop", "ping"} {
		if !saw[want] {
			t.Fatalf("missing SSE event %q in:\n%s", want, body)
		}
	}
	if text.String() != "Hello world" {
		t.Fatalf("anthropic text = %q", text.String())
	}
}

func TestE2EAnthropicNonStream(t *testing.T) {
	env := setupE2E(t)
	resp, body := postJSON(t, env.http.URL+"/v1/messages", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var out struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Fatalf("resp = %s", body)
	}
	var text string
	for _, c := range out.Content {
		if c.Type == "text" {
			text = c.Text
		}
	}
	if text != "Hello world" {
		t.Fatalf("text = %q body = %s", text, body)
	}
}

func TestE2EModelsAndAdmin(t *testing.T) {
	env := setupE2E(t)

	// /v1/models (OpenAI flavour)
	req, _ := http.NewRequest(http.MethodGet, env.http.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"e2e-model"`) {
		t.Fatalf("models: %d %s", resp.StatusCode, b)
	}

	// unauthorized
	req2, _ := http.NewRequest(http.MethodGet, env.http.URL+"/v1/models", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp2.StatusCode)
	}

	// admin add via bootstrap against the mock
	resp3, body3 := postJSON(t, env.http.URL+"/admin/accounts", "adm", map[string]any{
		"token_v2": "toknew", "label": "new",
	})
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("admin add: %d %s", resp3.StatusCode, body3)
	}
	var added struct {
		ID      string   `json:"id"`
		UserID  string   `json:"user_id"`
		Email   string   `json:"email"`
		SpaceID string   `json:"space_id"`
		Models  []string `json:"models"`
	}
	if err := json.Unmarshal(body3, &added); err != nil {
		t.Fatal(err)
	}
	if added.UserID != "u-good" || added.Email != "u-good@test.io" || added.SpaceID != "sp-good" {
		t.Fatalf("bootstrap discovery wrong: %+v", added)
	}

	// admin delete
	req4, _ := http.NewRequest(http.MethodDelete, env.http.URL+"/admin/accounts/"+added.ID, nil)
	req4.Header.Set("Authorization", "Bearer adm")
	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Fatalf("delete: %d", resp4.StatusCode)
	}

	// healthz
	resp5, err := http.Get(env.http.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp5.StatusCode)
	}
}

func TestE2EAllAccountsDown(t *testing.T) {
	env := setupE2E(t)
	// Kill the good account, leaving only bad/limited (which fail upstream).
	_ = env.pool.Update("acc-good", func(a *model.Account) { a.Status = model.StatusDisabled })
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model":    "e2e-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	// The last failing attempt was 429, so the proxy surfaces the rate limit;
	// other total-failure orders surface 502.
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 429/502 when every upstream attempt fails, got %d: %s", resp.StatusCode, body)
	}
}

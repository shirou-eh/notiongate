package api

import (
	"encoding/json"
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

// Мок где Notion возвращает tool_calls-JSON в тексте.
// Проверяет полный луп: клиентские тулзы (Edit/Bash) не отбрасываются,
// параметры/ID доезжают до агента, файлы правит агент локально.
func setupE2ETools(t *testing.T, notionText string) *e2eEnv {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		uid := "u-tool"
		w.Write([]byte(`{"` + uid + `":{"notion_user":{"` + uid + `":{"value":{"value":{"email":"tool@test.io"}}}}` +
			`,"space":{"sp-tool":{"spaceId":"sp-tool","value":{"value":{"id":"sp-tool","name":"WS","settings":{"enable_ai_feature":true}}}}}},` +
			`"space_view":{"sv-tool":{"spaceId":"sp-tool"}}}}`))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"model":"e2e-model","modelMessage":"E2E","isDisabled":false}]}`))
	})
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		// Проверяем что tool-результат с ID доехал в transcript (связка не рвётся).
		_, _ = io.ReadAll(r.Body)
		start, _ := json.Marshal(map[string]any{"type": "patch-start", "data": map[string]any{"s": []any{}}})
		patch, _ := json.Marshal(map[string]any{
			"type": "patch",
			"v": []any{map[string]any{
				"o": "a", "p": "/s/-",
				"v": map[string]any{"id": "m1", "type": "agent-inference",
					"value": []any{map[string]any{"type": "text", "content": notionText}}},
			}},
		})
		lines := []string{string(start), string(patch)}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e-tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: false, MaxAttempts: 3,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := p.Add(model.Account{ID: "acc-tool", Label: "tool", TokenV2: "toktool",
		UserID: "u-tool", SpaceID: "sp-tool", Status: model.StatusActive,
		Models: []string{"e2e-model"}, LastUsed: now}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)
	return &e2eEnv{notion: mock, http: ts, pool: p, st: st}
}

func TestE2EOpenAIToolsPassthrough(t *testing.T) {
	toolJSON := "```json\n{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Edit\",\"arguments\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}}]}\n```\nсоздаю файл"
	env := setupE2ETools(t, toolJSON)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "создай файл a.txt"},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "Edit", "description": "edit file", "parameters": map[string]any{"type": "object"}}},
			{"type": "function", "function": map[string]any{"name": "Bash", "description": "run", "parameters": map[string]any{"type": "object"}}},
		},
		"tool_choice": "auto",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected tool_calls finish: %s", body)
	}
	if len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool_call: %s", body)
	}
	tc := out.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "Edit" || tc.ID != "call_1" || !strings.Contains(tc.Function.Arguments, "a.txt") {
		t.Fatalf("wrong tc: %+v body=%s", tc, body)
	}
}

func TestE2EOpenAIToolsStream(t *testing.T) {
	toolJSON := "{\"tool_calls\":[{\"id\":\"call_9\",\"type\":\"function\",\"function\":{\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"ls\\\"}\"}}]}"
	env := setupE2ETools(t, toolJSON)
	resp, body := postJSON(t, env.http.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model", "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "запусти ls"}},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "Bash", "description": "run", "parameters": map[string]any{"type": "object"}}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	s := string(body)
	if !strings.Contains(s, "tool_calls") || !strings.Contains(s, "call_9") || !strings.Contains(s, "[DONE]") {
		t.Fatalf("stream must carry tool_calls: %s", s)
	}
}

func TestE2EAnthropicToolsPassthrough(t *testing.T) {
	toolJSON := "```json\n{\"tool_calls\":[{\"id\":\"toolu_1\",\"type\":\"function\",\"function\":{\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"ls\\\"}\"}}]}\n```"
	env := setupE2ETools(t, toolJSON)
	resp, body := postJSON(t, env.http.URL+"/v1/messages", "sk-test", map[string]any{
		"model": "e2e-model", "max_tokens": 100,
		"messages": []map[string]any{{"role": "user", "content": "запусти ls"}},
		"tools": []map[string]any{
			{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object"}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "tool_use") || !strings.Contains(string(body), "toolu_1") {
		t.Fatalf("expected tool_use: %s", body)
	}
}

// TestE2EOpenAIToolsTwoTurnLoop — полный агентный луп правки локального файла:
//  1. клиент просит создать файл → модель возвращает Edit tool_call →
//     прокси отдаёт его клиенту (файл создаёт АГЕНТ у себя локально);
//  2. клиент шлёт назад tool-результат («файл создан») → прокси обязан
//     довезти его в Notion-transcript с тем же ID → модель даёт финальный ответ.
//
// Мок проверяет что во втором запросе transcript реально содержит ID вызова
// и содержимое «созданного файла» — иначе связка порвана и агент зациклится.
func TestE2EOpenAIToolsTwoTurnLoop(t *testing.T) {
	toolJSON := "```json\n{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Edit\",\"arguments\":\"{\\\"path\\\":\\\"hello.txt\\\",\\\"content\\\":\\\"hi\\\"}\"}}]}\n```\nсоздаю файл"
	finalText := "Файл hello.txt создан."

	var mu struct {
		n      int
		bodies []string
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		uid := "u-tool"
		w.Write([]byte(`{"` + uid + `":{"notion_user":{"` + uid + `":{"value":{"value":{"email":"tool@test.io"}}}}` +
			`,"space":{"sp-tool":{"spaceId":"sp-tool","value":{"value":{"id":"sp-tool","name":"WS","settings":{"enable_ai_feature":true}}}}}},` +
			`"space_view":{"sv-tool":{"spaceId":"sp-tool"}}}}`))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"model":"e2e-model","modelMessage":"E2E","isDisabled":false}]}`))
	})
	emit := func(w http.ResponseWriter, text string) {
		start, _ := json.Marshal(map[string]any{"type": "patch-start", "data": map[string]any{"s": []any{}}})
		patch, _ := json.Marshal(map[string]any{
			"type": "patch",
			"v": []any{map[string]any{
				"o": "a", "p": "/s/-",
				"v": map[string]any{"id": "m1", "type": "agent-inference",
					"value": []any{map[string]any{"type": "text", "content": text}}},
			}},
		})
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, l := range []string{string(start), string(patch)} {
			_, _ = io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.n++
		mu.bodies = append(mu.bodies, string(body))
		if mu.n == 1 {
			emit(w, toolJSON) // шаг 1: модель просит создать файл
			return
		}
		emit(w, finalText) // шаг 2: модель видит результат и отвечает
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e-tools-loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: false, MaxAttempts: 3,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(model.Account{ID: "acc-tool", Label: "tool", TokenV2: "toktool",
		UserID: "u-tool", SpaceID: "sp-tool", Status: model.StatusActive,
		Models: []string{"e2e-model"}, LastUsed: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)

	toolsDef := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Edit", "description": "create/edit local file", "parameters": map[string]any{"type": "object"}}},
	}

	// --- шаг 1: «создай файл hello.txt» → ждём Edit tool_call ---
	resp1, body1 := postJSON(t, ts.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "создай файл hello.txt с текстом hi"},
		},
		"tools": toolsDef, "tool_choice": "auto",
	})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("step1 status=%d body=%s", resp1.StatusCode, body1)
	}
	var out1 struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body1, &out1); err != nil {
		t.Fatal(err)
	}
	if out1.Choices[0].FinishReason != "tool_calls" || len(out1.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("step1 must return Edit tool_call: %s", body1)
	}
	tc := out1.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "Edit" || !strings.Contains(tc.Function.Arguments, "hello.txt") {
		t.Fatalf("wrong tool_call: %+v", tc)
	}

	// --- «агент создаёт файл ЛОКАЛЬНО» (в тесте — просто фиксируем факт) ---
	localFileContent := "hi" // то что агент записал у себя на диске

	// --- шаг 2: отдаём результат назад как положено по протоколу ---
	resp2, body2 := postJSON(t, ts.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "создай файл hello.txt с текстом hi"},
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{
				{"id": tc.ID, "type": "function", "function": map[string]any{"name": "Edit", "arguments": tc.Function.Arguments}},
			}},
			{"role": "tool", "tool_call_id": tc.ID, "name": "Edit", "content": "файл создан, содержимое: " + localFileContent},
		},
		"tools": toolsDef, "tool_choice": "auto",
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("step2 status=%d body=%s", resp2.StatusCode, body2)
	}
	if !strings.Contains(string(body2), finalText) {
		t.Fatalf("step2 must return final text: %s", body2)
	}

	// --- мок подтверждает: ID и «содержимое файла» доехали до Notion ---
	if len(mu.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(mu.bodies))
	}
	if !strings.Contains(mu.bodies[1], tc.ID) {
		t.Fatalf("tool_call id lost in upstream transcript: %s", mu.bodies[1])
	}
	if !strings.Contains(mu.bodies[1], localFileContent) {
		t.Fatalf("local file content lost in upstream transcript: %s", mu.bodies[1])
	}
}

func TestE2EToolsModelEmitterHop(t *testing.T) {
	// Мок: reasoner (e2e-model) отвечает прозой без вызовов,
	// emitter (e2e-mini) отдаёт tool JSON. Проверяем tools_model-цепочку:
	// отказ первой модели -> вызовы от второй, ответ помечен tools.
	toolJSON := "```json\n{\"tool_calls\":[{\"id\":\"call_e\",\"type\":\"function\",\"function\":{\"name\":\"Edit\",\"arguments\":\"{\\\"path\\\":\\\"b.txt\\\"}\"}}]}\n```"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		uid := "u-tool"
		w.Write([]byte(`{"` + uid + `":{"notion_user":{"` + uid + `":{"value":{"value":{"email":"tool@test.io"}}}}` +
			`,"space":{"sp-tool":{"spaceId":"sp-tool","value":{"value":{"id":"sp-tool","name":"WS","settings":{"enable_ai_feature":true}}}}}},` +
			`"space_view":{"sv-tool":{"spaceId":"sp-tool"}}}}`))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"model":"e2e-model","modelMessage":"E2E","isDisabled":false},{"model":"e2e-mini","modelMessage":"E2E mini","isDisabled":false}]}`))
	})
	emit := func(w http.ResponseWriter, text string) {
		start, _ := json.Marshal(map[string]any{"type": "patch-start", "data": map[string]any{"s": []any{}}})
		patch, _ := json.Marshal(map[string]any{
			"type": "patch",
			"v": []any{map[string]any{
				"o": "a", "p": "/s/-",
				"v": map[string]any{"id": "m1", "type": "agent-inference",
					"value": []any{map[string]any{"type": "text", "content": text}}},
			}},
		})
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, l := range []string{string(start), string(patch)} {
			_, _ = io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := toolJSON
		if !strings.Contains(string(body), `"model":"e2e-mini"`) {
			text = "plain refusal prose, no calls here"
		}
		emit(w, text)
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e-tools-emit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: false, MaxAttempts: 3,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(model.Account{ID: "acc-tool", Label: "tool", TokenV2: "toktool",
		UserID: "u-tool", SpaceID: "sp-tool", Status: model.StatusActive,
		Models: []string{"e2e-model", "e2e-mini"}, LastUsed: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)

	resp, body := postJSON(t, ts.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "создай файл b.txt"},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "Edit", "description": "edit", "parameters": map[string]any{"type": "object"}}},
		},
		"tool_choice": "auto",
		"tools_model": "e2e-mini",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Choices[0].FinishReason != "tool_calls" || len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("emitter hop must produce tool_calls: %s", body)
	}
	if out.Choices[0].Message.ToolCalls[0].Function.Name != "Edit" {
		t.Fatalf("wrong call: %s", body)
	}
}

func TestE2EToolsModelNoDupMidChain(t *testing.T) {
	// История уже содержит tool result + tools_model задан: hop обязан НЕ
	// стрелять повторно (иначе двойная запись файла). Ровно 1 апстрим-вызов.
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		uid := "u-tool"
		w.Write([]byte(`{"` + uid + `":{"notion_user":{"` + uid + `":{"value":{"value":{"email":"tool@test.io"}}}}` +
			`,"space":{"sp-tool":{"spaceId":"sp-tool","value":{"value":{"id":"sp-tool","name":"WS","settings":{"enable_ai_feature":true}}}}}},` +
			`"space_view":{"sv-tool":{"spaceId":"sp-tool"}}}}`))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"model":"e2e-model","modelMessage":"E2E","isDisabled":false},{"model":"e2e-mini","modelMessage":"E2E mini","isDisabled":false}]}`))
	})
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body)
		start, _ := json.Marshal(map[string]any{"type": "patch-start", "data": map[string]any{"s": []any{}}})
		patch, _ := json.Marshal(map[string]any{
			"type": "patch",
			"v": []any{map[string]any{
				"o": "a", "p": "/s/-",
				"v": map[string]any{"id": "m1", "type": "agent-inference",
					"value": []any{map[string]any{"type": "text", "content": "Done, file written."}}},
			}},
		})
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, l := range []string{string(start), string(patch)} {
			_, _ = io.WriteString(w, l+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e-tools-nodup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: false, MaxAttempts: 3,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(model.Account{ID: "acc-tool", Label: "tool", TokenV2: "toktool",
		UserID: "u-tool", SpaceID: "sp-tool", Status: model.StatusActive,
		Models: []string{"e2e-model", "e2e-mini"}, LastUsed: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)

	resp, body := postJSON(t, ts.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model": "e2e-model",
		"messages": []map[string]any{
			{"role": "user", "content": "создай файл b.txt"},
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{
				{"id": "call_1", "type": "function", "function": map[string]any{"name": "Edit", "arguments": "{}"}},
			}},
			{"role": "tool", "tool_call_id": "call_1", "name": "Edit", "content": "written"},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "Edit", "description": "edit", "parameters": map[string]any{"type": "object"}}},
		},
		"tool_choice": "auto",
		"tools_model": "e2e-mini",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if calls != 1 {
		t.Fatalf("mid-chain must cost exactly 1 upstream call, got %d", calls)
	}
	if !strings.Contains(string(body), "Done, file written.") {
		t.Fatalf("expected text answer: %s", body)
	}
}

func TestE2EEmptyStreakCapsFailover(t *testing.T) {
	// Все аккаунты отдают пустые стримы (гейт модели/троттлинг):
	// перебор обязан остановиться на 3, а не положить весь пул.
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/getSpaces", func(w http.ResponseWriter, r *http.Request) {
		uid := "u-e"
		w.Write([]byte(`{"` + uid + `":{"notion_user":{"` + uid + `":{"value":{"value":{"email":"e@test.io"}}}}` +
			`,"space":{"sp-e":{"spaceId":"sp-e","value":{"value":{"id":"sp-e","name":"WS","settings":{"enable_ai_feature":true}}}}}},` +
			`"space_view":{"sv-e":{"spaceId":"sp-e"}}}}`))
	})
	mux.HandleFunc("POST /api/v3/getAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"model":"e2e-model","modelMessage":"E2E","isDisabled":false}]}`))
	})
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body)
		// пустой 200-стрим: ни content, ни ошибок
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{\"type\":\"patch-start\",\"data\":{\"s\":[]}}\n")
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "e2e-empty-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Host: "127.0.0.1", Port: 0,
		APIKey: "sk-test", AdminKey: "adm",
		RotateAt: 0.8, DefaultWindow: "month", DefaultLimit: 0,
		StickySessions: false, MaxAttempts: 32,
		UpstreamTimeout: 30 * time.Second, RefreshInterval: time.Hour,
		NotionBaseURL: mock.URL, NotionClientVersion: "test", UserAgent: "ua",
	}
	p, err := pool.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := strings.Repeat(string(rune('a'+i)), 8) + "-0000-0000-0000-000000000000"
		if err := p.Add(model.Account{ID: id, Label: "e", TokenV2: "tok", UserID: "u-e",
			SpaceID: "sp-e", Status: model.StatusActive,
			Models: []string{"e2e-model"}, LastUsed: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(New(cfg, p, st).Handler())
	t.Cleanup(ts.Close)

	resp, _ := postJSON(t, ts.URL+"/v1/chat/completions", "sk-test", map[string]any{
		"model":    "e2e-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatal("empty streams must not succeed")
	}
	if calls != 3 {
		t.Fatalf("failover must stop after 3 empties, got %d upstream calls", calls)
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/tools"
	"github.com/shirou-eh/notiongate/internal/translate"
)

// handleOpenAIChat implements POST /v1/chat/completions.
func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, 4<<20, false)
	if !ok {
		return
	}
	job, err := translate.ParseOpenAI(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_body")
		return
	}
	if job.Model != "" && !s.pool.IsKnownModel(job.Model) {
		writeError(w, http.StatusBadRequest,
			"unknown model "+quoteModel(job.Model)+" (use GET /v1/models)",
			"invalid_request_error", "model_not_found")
		return
	}
	if !hasMeaningfulContent(job) {
		writeError(w, http.StatusBadRequest, "messages contain no text content", "invalid_request_error", "empty_messages")
		return
	}
	prepareJob(r.Context(), job)
	if tr := translate.BuildTranscript(job); len(tr) == 0 {
		writeError(w, http.StatusBadRequest, "messages contain no text content", "invalid_request_error", "empty_messages")
		return
	}
	if job.Stream {
		s.openAIStream(w, r, job)
		return
	}
	s.openAIBlock(w, r, job)
}

func (s *Server) openAIBlock(w http.ResponseWriter, r *http.Request, job *translate.ChatJob) {
	var events []notion.Event
	res, err := s.runInference(r.Context(), job, func(ev notion.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		s.writeRunError(w, r, err)
		return
	}
	id := translate.NewCompletionID()
	// Авто-извлечение тулзов — всегда пробуем, даже если клиент не просил.
	// Если модель вернула JSON с tool_calls, отдаём их в правильном формате.
	text := notion.FullText(events)
	if len(job.Tools) > 0 || len(text) > 0 {
		if calls, clean, ok := s.toolsBridge().Extract(text); ok {
			writeJSON(w, http.StatusOK, translate.BuildOpenAIResponseWithTools(id, job.Model, calls, clean, notion.ReasoningText(events), res.InTok, res.OutTok))
			return
		}
	}
	writeJSON(w, http.StatusOK, translate.BuildOpenAIResponse(id, job.Model, events, res.InTok, res.OutTok))
}

func (s *Server) toolsBridge() *tools.Bridge {
	return tools.DefaultBridge
}

func (s *Server) openAIStream(w http.ResponseWriter, r *http.Request, job *translate.ChatJob) {
	id := translate.NewCompletionID()
	var (
		wroteHeader bool
		firstChunk  = true
	)

	forward := func(ev notion.Event) error {
		var contentDelta, reasoningDelta string
		switch ev.Kind {
		case notion.EventText:
			contentDelta = ev.Text
		case notion.EventReasoning:
			reasoningDelta = ev.Text
		default:
			return nil // search queries and metadata are not streamed in v1
		}
		if contentDelta == "" && reasoningDelta == "" {
			return nil
		}
		if !wroteHeader {
			writeSSEHeaders(w)
			wroteHeader = true
		}
		if firstChunk {
			firstChunk = false
			// OpenAI clients expect the role in the first delta.
			if err := writeSSEData(w, translate.BuildOpenAIChunk(id, job.Model, true, "", "", nil)); err != nil {
				return err
			}
		}
		return writeSSEData(w, translate.BuildOpenAIChunk(id, job.Model, false, contentDelta, reasoningDelta, nil))
	}

	res, err := s.runInference(r.Context(), job, forward)

	if !wroteHeader {
		// Nothing was streamed: safe to answer with a regular JSON error.
		if err != nil {
			s.writeRunError(w, r, err)
			return
		}
		writeSSEHeaders(w)
		wroteHeader = true
	}
	if err == nil && job.StreamUsage {
		// stream_options.include_usage: final chunk with usage, empty choices.
		usageChunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model":   job.Model,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens": res.InTok, "completion_tokens": res.OutTok,
				"total_tokens": res.InTok + res.OutTok,
			},
		}
		b, _ := json.Marshal(usageChunk)
		_ = writeSSEData(w, b)
	}
	if err != nil {
		// Mid-stream failure: emit a non-standard error chunk (sanitized —
		// raw upstream text must not reach the client), then close.
		slog.Warn("api: mid-stream failure", "err", err)
		_ = writeSSEData(w, []byte(`{"error":{"message":"upstream stream failed","type":"api_error","code":"upstream"}}`))
		_ = writeSSEData(w, []byte("[DONE]"))
		return
	}
	finish := "stop"
	_ = writeSSEData(w, translate.BuildOpenAIChunk(id, job.Model, false, "", "", &finish))
	_ = writeSSEData(w, []byte("[DONE]"))
	_ = res // result already recorded by the executor
}

// writeRunError maps executor errors onto client-facing HTTP errors.
func (s *Server) writeRunError(w http.ResponseWriter, r *http.Request, err error) {
	status, errType, code := http.StatusBadGateway, "api_error", "upstream"
	switch {
	case errors.Is(err, pool.ErrUnknownModel):
		status, errType, code = http.StatusBadRequest, "invalid_request_error", "model_not_found"
	case errors.Is(err, pool.ErrNoAccounts):
		status, errType, code = http.StatusServiceUnavailable, "server_error", "no_accounts"
	case errors.Is(err, notion.ErrAuth):
		status, code = http.StatusBadGateway, "upstream_auth"
	case errors.Is(err, notion.ErrAINotEnabled):
		status, code = http.StatusForbidden, "ai_not_enabled"
	case errors.Is(err, pool.ErrTooBusy):
		status, code = http.StatusServiceUnavailable, "too_busy"
	case errors.Is(err, notion.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "timeout"
	}
	msg := err.Error()
	if errors.Is(err, pool.ErrUnknownModel) {
		msg = "unknown model (use GET /v1/models)"
	}
	if code == "ai_not_enabled" {
		// Sanitized: no raw upstream reflection, even for actionable errors.
		// Сюда попадаем только когда ВСЕ аккаунты перебраны — каждый
		// отдельный ai_not_enabled до этого скипается с failover.
		msg = "Notion AI is not enabled for this account/space (code: ai_not_enabled)"
	}
	if errors.Is(err, pool.ErrNoAccounts) {
		msg = "no usable Notion accounts in the pool (all exhausted, cooling down or invalid)"
	}
	if code == "upstream" || code == "upstream_auth" {
		// Never reflect raw upstream internals to clients; details in logs.
		slog.Warn("api: upstream failure", "code", code, "err", err)
		msg = "Notion upstream request failed (" + code + ")"
	}
	if code == "too_busy" || code == "rate_limited" {
		// Capacity signals must carry a retry hint so clients back off
		// instead of retrying immediately.
		w.Header().Set("Retry-After", "5")
	}
	if r.URL.Path == "/v1/messages" {
		anthType := map[string]string{
			"invalid_request_error": "invalid_request_error",
			"authentication_error":  "authentication_error",
			"rate_limited":          "rate_limit_error",
			"timeout":               "timeout_error",
		}[errType]
		if anthType == "" {
			anthType = "api_error"
		}
		writeJSON(w, status, translate.BuildAnthropicError(anthType, msg))
		return
	}
	writeError(w, status, msg, errType, code)
}

// writeSSEHeaders sets the streaming response headers.
func writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func writeSSEData(w http.ResponseWriter, payload []byte) error {
	if _, err := w.Write(append([]byte("data: "), payload...)); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// handleModels serves GET /v1/models in OpenAI or Anthropic flavour depending
// on the client. Both SDKs call the same path.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ids := s.modelIDs()
	if r.Header.Get("anthropic-version") != "" {
		writeJSON(w, http.StatusOK, translate.BuildAnthropicModels(ids))
		return
	}
	writeJSON(w, http.StatusOK, translate.BuildOpenAIModels(ids))
}

// quoteModel renders a client-supplied model name safely in errors
// (длина ограничена чтобы не раздувать ответ).
func quoteModel(m string) string {
	if len(m) > 64 {
		m = m[:64] + "…"
	}
	q, _ := json.Marshal(m)
	return string(q)
}

// modelIDs returns the union of model ids:
//  1. live per-account discovery (только enabled — bootstrap уже отфильтровал),
//  2. verified KnownModels fallback (только enabled, без disabled fable-5).
//
// Легаси-мусор ("probe-inconclusive", "default", "auto", "notiongate")
// отбрасывается: это были фейковые id которые молча уходили в дефолт.
func (s *Server) modelIDs() []string {
	seen := map[string]bool{}
	var ids []string
	for _, v := range s.pool.Snapshot() {
		for _, m := range v.Models {
			if m == "" || isLegacyFakeModel(m) {
				continue
			}
			if !seen[m] {
				seen[m] = true
				ids = append(ids, m)
			}
		}
	}
	// Fallback/union с проверенной таблицей: пул может быть пуст или
	// discovery ещё не отработал — клиент всё равно видит ВСЕ реальные модели.
	for _, k := range notion.EnabledFriendlyNames() {
		if !seen[k] {
			seen[k] = true
			ids = append(ids, k)
		}
	}
	sort.Strings(ids)
	return ids
}

func isLegacyFakeModel(m string) bool {
	switch m {
	case "probe-inconclusive", "default", "auto", "notiongate", "notion-ai",
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "o1", "o1-mini",
		"gpt-5", "deepseek-v3", "deepseek-r1", "qwen-3-max", "grok-4",
		"kimi-k2", "kimi-k2.5", "kimi-2.6",
		"claude-3.5-sonnet", "claude-3-opus", "claude-sonnet-4", "claude-sonnet-4.5",
		"claude-opus-4.5", "gemini-2.0-flash", "gemini-1.5-pro", "gemini-3-pro":
		return true
	}
	return false
}

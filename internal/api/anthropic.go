package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/translate"
)

// handleAnthropicMessages implements POST /v1/messages (Anthropic Messages API).
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, 4<<20, true)
	if !ok {
		return
	}
	job, err := translate.ParseAnthropic(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, translate.BuildAnthropicError("invalid_request_error", err.Error()))
		return
	}
	if job.Model != "" && !s.pool.IsKnownModel(job.Model) {
		writeJSON(w, http.StatusBadRequest, translate.BuildAnthropicError("invalid_request_error",
			"unknown model "+job.Model+" (use GET /v1/models)"))
		return
	}
	if !hasMeaningfulContent(job) {
		writeJSON(w, http.StatusBadRequest, translate.BuildAnthropicError("invalid_request_error", "messages contain no text content"))
		return
	}
	prepareJob(r.Context(), job)
	if tr := translate.BuildTranscript(job); len(tr) == 0 {
		writeJSON(w, http.StatusBadRequest, translate.BuildAnthropicError("invalid_request_error", "messages contain no text content"))
		return
	}
	if job.Stream {
		s.anthropicStream(w, r, job)
		return
	}
	s.anthropicBlock(w, r, job)
}

func (s *Server) anthropicBlock(w http.ResponseWriter, r *http.Request, job *translate.ChatJob) {
	var events []notion.Event
	res, err := s.runInference(r.Context(), job, func(ev notion.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		s.writeRunError(w, r, err)
		return
	}
	id := translate.NewMessageID()
	text := notion.FullText(events)
	if len(job.Tools) > 0 {
		if calls, clean, ok := s.toolsBridge().Extract(text); ok {
			writeJSON(w, http.StatusOK, translate.BuildAnthropicResponseWithTools(id, job.Model, calls, clean, res.InTok, res.OutTok))
			return
		}
	}
	writeJSON(w, http.StatusOK, translate.BuildAnthropicResponse(id, job.Model, events, res.InTok, res.OutTok))
}

// anthropicStream implements Anthropic SSE: message_start, content blocks,
// message_delta, message_stop. Thinking and text map to separate blocks.
func (s *Server) anthropicStream(w http.ResponseWriter, r *http.Request, job *translate.ChatJob) {
	id := translate.NewMessageID()
	var (
		wroteHeader bool
		started     bool // message_start + ping sent
		blockKind   string
		blockIndex  int
	)

	start := func() {
		if !wroteHeader {
			writeSSEHeaders(w)
			wroteHeader = true
		}
		if !started {
			started = true
			_ = writeSSEEvent(w, "message_start", translate.BuildAnthropicMessageStart(id, job.Model, 0))
			_ = writeSSEEvent(w, "ping", translate.BuildAnthropicPing())
		}
	}

	closeBlock := func() {
		if blockKind != "" {
			_ = writeSSEEvent(w, "content_block_stop", translate.BuildAnthropicBlockStop(blockIndex))
			blockIndex++
			blockKind = ""
		}
	}

	forward := func(ev notion.Event) error {
		kind := ""
		switch ev.Kind {
		case notion.EventText:
			kind = "text"
		case notion.EventReasoning:
			kind = "thinking"
		default:
			return nil
		}
		start()
		if kind != blockKind {
			closeBlock()
			blockKind = kind
			block := translate.AnthropicBlock{Type: kind}
			_ = writeSSEEvent(w, "content_block_start", translate.BuildAnthropicBlockStart(blockIndex, block))
		}
		var payload []byte
		if kind == "text" {
			payload = translate.BuildAnthropicTextDelta(blockIndex, ev.Text)
		} else {
			payload = translate.BuildAnthropicThinkingDelta(blockIndex, ev.Text)
		}
		return writeSSEEvent(w, "content_block_delta", payload)
	}

	res, err := s.runInference(r.Context(), job, forward)

	if !wroteHeader {
		if err != nil {
			s.writeRunError(w, r, err)
			return
		}
		start()
	}
	if err != nil {
		// Sanitized: raw upstream text must not reach the client. Per the
		// Anthropic SSE contract the error event is terminal — no block/delta
		// events may follow it.
		slog.Warn("api: mid-stream failure", "err", err)
		_ = writeSSEEvent(w, "error", translate.BuildAnthropicError("api_error", "upstream stream failed"))
		return
	}
	closeBlock()
	_ = writeSSEEvent(w, "message_delta", translate.BuildAnthropicMessageDelta(res.OutTok))
	_ = writeSSEEvent(w, "message_stop", translate.BuildAnthropicMessageStop())
}

func writeSSEEvent(w http.ResponseWriter, event string, payload []byte) error {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(payload)
	b.WriteString("\n\n")
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

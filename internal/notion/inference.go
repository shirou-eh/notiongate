package notion

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
)

// Event is a normalized streaming event produced by an inference call.
type Event struct {
	Kind      EventKind `json:"kind"`
	Text      string    `json:"text,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	TokensIn  int64     `json:"tokens_in,omitempty"`
	TokensOut int64     `json:"tokens_out,omitempty"`
	// Err is set when Kind == EventError.
	Err error `json:"-"`
}

type EventKind string

const (
	EventText      EventKind = "text"
	EventReasoning EventKind = "reasoning"
	EventSearch    EventKind = "search_queries"
	EventUsage     EventKind = "usage"
	EventDone      EventKind = "done"
	EventError     EventKind = "error"
)

// ErrAINotEnabled marks spaces where Notion AI is not enabled.
var ErrAINotEnabled = errors.New("notion: AI not enabled on this space")

// ErrModelDisabled marks a model restricted by plan (e.g. Fable 5 on
// non-Business/Enterprise). В отличие от ai_not_enabled это ограничение
// модели, а не space: аккаунт НЕ отправляем в cooldown.
var ErrModelDisabled = errors.New("notion: model disabled for this plan")

// isAINotEnabledMessage detects all known spellings of the upstream
// "AI not enabled" signal (case-insensitive): aiNotEnabled,
// AiNotEnabledOnSpace..., ai_not_enabled, ai-not-enabled, "ai not enabled".
func isAINotEnabledMessage(msg string) bool {
	lower := strings.ToLower(msg)
	compact := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, lower)
	return strings.Contains(compact, "ainotenabled")
}

// isModelDisabledMessage detects plan-restricted model signals.
func isModelDisabledMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "premium-feature-unavailable") ||
		strings.Contains(lower, "premium feature unavailable") ||
		strings.Contains(lower, "business_or_enterprise_plan_required") ||
		strings.Contains(lower, "business or enterprise")
}

// inferenceErrFromMessage maps well-known upstream error strings onto
// classified sentinels.
func inferenceErrFromMessage(msg string) error {
	if isAINotEnabledMessage(msg) {
		return &InferenceError{Err: fmt.Errorf("%w: %s", ErrAINotEnabled, msg)}
	}
	if isModelDisabledMessage(msg) {
		return &InferenceError{Err: fmt.Errorf("%w: %s", ErrModelDisabled, msg)}
	}
	return &InferenceError{Err: fmt.Errorf("upstream: %s", msg)}
}

// InferenceError is a mid-stream failure.
type InferenceError struct{ Err error }

func (e *InferenceError) Error() string { return "notion: stream failed: " + e.Err.Error() }
func (e *InferenceError) Unwrap() error { return e.Err }

// ---- transcript blocks (2026 chat-panel protocol) ----

// TranscriptEntry is one block of a Notion AI transcript.
type TranscriptEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // config | context | user | agent-inference
	Value     any    `json:"value,omitempty"`
	UserID    string `json:"userId,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

type TranscriptContent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// InferenceRequest describes one runInferenceTranscript call.
type InferenceRequest struct {
	SpaceID     string
	UserID      string
	Email       string
	UserName    string
	SpaceName   string
	SpaceViewID string
	Model       string // Notion-internal model codename (empty → server default)
	Transcript  []TranscriptEntry
	// ToolsJSON — спеки тулзов для config-плейсмента (JSON-массив
	// [{name,description,parameters}]). Пусто = обычный system-инжект.
	ToolsJSON string
	// IsProbe marks bootstrap probes: skips title generation.
	IsProbe bool
}

// RunInferenceStream posts runInferenceTranscript and returns a channel of
// normalized events. Request-level failures (auth/limit/network/HTTP status)
// are returned as an error immediately so the caller can fail over to another
// account; failures after the stream started are delivered as EventError.
//
// The returned cancel func releases upstream resources and must be called
// when the caller stops consuming early.
func (c *Client) RunInferenceStream(ctx context.Context, req *InferenceRequest) (<-chan Event, func(), error) {
	traceID := model.NewID()
	threadID := model.NewID()
	sid := pickStr(req.SpaceID, c.spaceID)
	turns := req.Transcript
	if len(turns) == 0 {
		turns = []TranscriptEntry{UserBlock("Hello")}
	}
	transcript := make([]TranscriptEntry, 0, len(turns)+2)
	transcript = append(transcript,
		ConfigBlock(req.Model, req.ToolsJSON),
		ContextBlock(req.UserID, req.Email, req.UserName, sid, req.SpaceName, req.SpaceViewID),
		UserSpecifiedContextBlock(),
	)
	transcript = append(transcript, turns...)
	// Process file attachments: download and upload to Notion so they become real file blocks
	transcript = c.processFileBlocks(ctx, transcript)
	// userId на user-блоках — так шлёт настоящий веб-клиент (видно в HAR:
	// {"type":"user","value":[[...]],"userId":"...","createdAt":"..."}).
	// Без него мультишаговые треды теряют авторство.
	uid := pickStr(req.UserID, c.userID)
	if uid != "" {
		for i := range transcript {
			if transcript[i].Type == "user" && transcript[i].UserID == "" {
				transcript[i].UserID = uid
			}
		}
	}

	payload := map[string]any{
		"traceId":                       traceID,
		"spaceId":                       sid,
		"transcript":                    transcript,
		"threadId":                      threadID,
		"createThread":                  true,
		"isPartialTranscript":           false,
		"generateTitle":                 !req.IsProbe,
		"saveAllThreadOperations":       true,
		"setUnreadState":                true,
		"threadType":                    "workflow",
		"asPatchResponse":               true,
		"patchResponseVersion":          2,
		"hasHeartbeat":                  false,
		"createdSource":                 "ai_module",
		"isUserInAnySalesAssistedSpace": false,
		"isSpaceSalesAssisted":          false,
	}
	payload["threadParentPointer"] = map[string]any{
		"table": "space", "id": sid, "spaceId": sid,
	}
	payload["debugOverrides"] = map[string]any{
		"emitAgentSearchExtractedResults": true,
		"cachedInferences":                map[string]any{},
		"annotationInferences":            map[string]any{},
		"emitInferences":                  false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("notion: encode payload: %w", err)
	}

	// The stream context drives BOTH the HTTP request and body reads, so
	// cancel() aborts everything, and the upstream timeout always applies.
	// cancelT is released by the reader goroutine when the stream ends (or by
	// the returned cancel func on early abort) — NOT by this function, which
	// must return while the stream is still running.
	streamCtx, cancel := context.WithCancel(ctx)
	var cancelT context.CancelFunc = func() {}
	if c.timeout > 0 {
		streamCtx, cancelT = context.WithTimeout(streamCtx, c.timeout)
	}
	stop := func() { cancel(); cancelT() }

	resp, err := c.doInference(streamCtx, body, sid)
	if err != nil {
		stop()
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		// Читаем тело чтобы отличить aiNotEnabled (скип) от auth (invalid):
		// Notion отдаёт 403 и с тем, и с другим.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		stop()
		return nil, nil, classifyWithBody(resp, errBody)
	}

	events := make(chan Event, 128)
	go func() {
		defer cancelT()
		defer close(events)
		defer resp.Body.Close()
		defer func() {
			// A malformed/malicious stream must never crash the process.
			if r := recover(); r != nil {
				select {
				case events <- Event{Kind: EventError, Err: &InferenceError{Err: fmt.Errorf("parser panic: %v", r)}}:
				case <-streamCtx.Done():
				}
			}
		}()
		p := &streamParser{}
		if err := p.consume(streamCtx, resp.Body, events); err != nil {
			select {
			case events <- Event{Kind: EventError, Err: &InferenceError{Err: err}}:
			case <-streamCtx.Done():
			}
		}
		p.emitFinal(events, streamCtx)
		select {
		case events <- Event{Kind: EventDone}:
		case <-streamCtx.Done():
		}
	}()
	return events, stop, nil
}

func pickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *Client) processFileBlocks(ctx context.Context, transcript []TranscriptEntry) []TranscriptEntry {
	out := make([]TranscriptEntry, 0, len(transcript))
	for _, e := range transcript {
		if e.Type != "file" {
			out = append(out, e)
			continue
		}
		// File block: Value is map with url/filename/content_type
		m, ok := e.Value.(map[string]any)
		if !ok {
			continue
		}
		urlStr, _ := m["url"].(string)
		filename, _ := m["filename"].(string)
		ctype, _ := m["content_type"].(string)
		if urlStr == "" {
			continue
		}
		if filename == "" {
			filename = "file"
		}
		if ctype == "" || ctype == "image/*" {
			ctype = "image/jpeg"
		}
		if strings.HasPrefix(ctype, "image/") && ctype == "image/*" {
			ctype = "image/jpeg"
		}
		// Handle data URL
		var data []byte
		var err error
		if strings.HasPrefix(urlStr, "data:") {
			// data:image/jpeg;base64,...
			parts := strings.SplitN(urlStr, ",", 2)
			if len(parts) == 2 {
				// Check if base64
				if strings.Contains(parts[0], "base64") {
					data, err = decodeBase64(parts[1])
					if err != nil {
						continue
					}
					// Extract content type from data URL
					if ct := strings.Split(strings.TrimPrefix(parts[0], "data:"), ";")[0]; ct != "" {
						ctype = ct
					}
				} else {
					data = []byte(parts[1])
				}
			}
		} else {
			// Download from URL
			req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
			if err != nil {
				continue
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue
			}
			data, err = io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
			resp.Body.Close()
			if err != nil || len(data) == 0 {
				continue
			}
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				ctype = strings.Split(ct, ";")[0]
			}
		}
		if len(data) == 0 {
			continue
		}
		// Upload to Notion
		ref, err := c.UploadFile(ctx, data, filename, ctype)
		if err != nil {
			// Fallback: keep as text placeholder
			out = append(out, TranscriptEntry{
				ID:    e.ID,
				Type:  "user",
				Value: [][]string{{"[file: " + filename + " (" + ctype + ")]"}},
			})
			continue
		}
		// Replace with uploaded file block
		out = append(out, TranscriptEntry{
			ID:   ref.ID,
			Type: "file",
			Value: map[string]any{
				"id":   ref.ID,
				"url":  ref.URL,
				"name": ref.Name,
				"type": ref.Type,
			},
		})
	}
	return out
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Fallback to raw (no padding)
	s = strings.TrimRight(s, "=")
	return base64.RawStdEncoding.DecodeString(s)
}

// streamParser turns the 2026 patch-stream into events.
//
// Line types: patch-start, patch (v: ops a/x/p), agent-inference (cumulative
// mode), error, premium-feature-unavailable, record-map / heartbeat / others
// (ignored). Text/thinking arrive as ops against /s/N/value/M/content.
type streamParser struct {
	sectionCount int
	valueTypes   map[string]string // "/s/N/value/M" → declared type
	valueCounts  map[string]int    // "/s/N" → number of value slots seen

	lastText     string // last cumulative agent-inference text (non-patch mode)
	lastThinking string

	emittedText bool // any EventText sent (for fallback suppression)
	inTok       int64
	outTok      int64
}

func (p *streamParser) consume(ctx context.Context, r io.Reader, out chan<- Event) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, eof, err := readLimitedLine(br, maxLineBytes)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(line)) > 0 {
			abort, sendErr := p.processLine(ctx, line, out)
			if abort || sendErr != nil {
				return sendErr
			}
		}
		if eof {
			return nil
		}
	}
}

// maxLineBytes caps a single NDJSON line; a hostile/garbled upstream cannot
// force unbounded allocations through line buffering.
const maxLineBytes = 16 << 20

// readLimitedLine reads one \n-terminated line with a hard byte cap that is
// enforced on every chunk (not only on ErrBufferFull).
func readLimitedLine(br *bufio.Reader, limit int) (line string, eof bool, err error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > limit {
			// Discard the remainder of the oversized line.
			for err == bufio.ErrBufferFull {
				_, err = br.ReadSlice('\n')
			}
			if err == io.EOF {
				return "", true, nil
			}
			if err != nil {
				return "", false, err
			}
			return "", false, fmt.Errorf("ndjson line exceeds %d bytes", limit)
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			if len(buf) > limit {
				// Discard the remainder of the oversized line.
				for err == bufio.ErrBufferFull {
					_, err = br.ReadSlice('\n')
				}
				if err == io.EOF {
					return "", true, nil
				}
				if err != nil {
					return "", false, err
				}
				return "", false, fmt.Errorf("ndjson line exceeds %d bytes", limit)
			}
			continue
		}
		if err == io.EOF {
			if len(buf) > 0 {
				return string(buf), true, nil
			}
			return "", true, nil
		}
		if err != nil {
			return "", false, err
		}
		return string(buf), false, nil
	}
}

// send delivers an event, aborting when the stream context is cancelled.
func send(ctx context.Context, out chan<- Event, ev Event) error {
	select {
	case out <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processLine handles one NDJSON line; returns true on terminal error events.
func (p *streamParser) processLine(ctx context.Context, line string, out chan<- Event) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, nil
	}
	var top map[string]any
	if json.Unmarshal([]byte(line), &top) != nil {
		return false, nil // tolerate junk lines
	}
	t, _ := top["type"].(string)

	switch t {
	case "error":
		msg := eventMessage(top)
		return true, send(ctx, out, Event{Kind: EventError, Err: inferenceErrFromMessage(msg)})
	case "premium-feature-unavailable":
		msg := eventMessage(top)
		if msg == "" || msg == "unknown notion error" {
			msg = "premium-feature-unavailable"
		}
		return true, send(ctx, out, Event{Kind: EventError, Err: inferenceErrFromMessage(msg)})
	case "patch-start":
		p.handlePatchStart(top)
		return false, nil
	case "patch":
		return p.handlePatch(ctx, top, out)
	case "agent-inference":
		p.handleAgentInference(ctx, top, out)
		return false, nil
	}
	return false, nil
}

func eventMessage(top map[string]any) string {
	if m, ok := top["message"].(string); ok && m != "" {
		return m
	}
	if d, ok := top["data"].(string); ok && d != "" {
		return d
	}
	if b, err := json.Marshal(top); err == nil && len(b) < 300 {
		return string(b)
	}
	return "unknown notion error"
}

func (p *streamParser) handlePatchStart(top map[string]any) {
	data, _ := top["data"].(map[string]any)
	s, _ := data["s"].([]any)
	if p.valueCounts == nil {
		p.valueCounts = map[string]int{}
	}
	p.valueTypes = map[string]string{} // stale types must not misroute new streams
	p.sectionCount = len(s)
	for i := range s {
		p.valueCounts[sectionKey(i)] = 0
	}
}

func sectionKey(i int) string { return fmt.Sprintf("/s/%d", i) }

func (p *streamParser) handlePatch(ctx context.Context, top map[string]any, out chan<- Event) (bool, error) {
	ops, _ := top["v"].([]any)
	for _, raw := range ops {
		op, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		o, _ := op["o"].(string)
		pth, _ := op["p"].(string)
		v := op["v"]
		if o == "" || pth == "" {
			continue
		}
		// 0) new section appended at /s/-
		if o == "a" && pth == "/s/-" {
			vmap, ok := v.(map[string]any)
			if !ok {
				p.sectionCount++
				continue
			}
			if et, _ := vmap["type"].(string); et == "agent-tool-result" {
				_ = send(ctx, out, Event{Kind: EventSearch})
				p.sectionCount++
				continue
			}
			if et, _ := vmap["type"].(string); et == "error" {
				msg, _ := vmap["message"].(string)
				return true, send(ctx, out, Event{Kind: EventError, Err: inferenceErrFromMessage(msg)})
			}
			p.absorbInlineSection(p.sectionCount, vmap, ctx, out)
			p.sectionCount++
			continue
		}
		// 1) new value entry at /s/N/value/-
		if o == "a" && strings.Contains(pth, "/value/-") {
			vmap, ok := v.(map[string]any)
			if !ok {
				continue
			}
			prefix := pth[:strings.Index(pth, "/value/")]
			if p.valueCounts == nil {
				p.valueCounts = map[string]int{}
			}
			idx := p.valueCounts[prefix]
			et, _ := vmap["type"].(string)
			if et != "" {
				if p.valueTypes == nil {
					p.valueTypes = map[string]string{}
				}
				p.valueTypes[fmt.Sprintf("%s/value/%d", prefix, idx)] = et
			}
			p.valueCounts[prefix] = idx + 1
			if content, ok := vmap["content"].(string); ok && content != "" {
				if err := p.emitTyped(ctx, out, et, content); err != nil {
					return false, err
				}
			}
			continue
		}
		// 2) real usage tokens
		if o == "a" {
			if n, ok := v.(float64); ok {
				switch {
				case strings.HasSuffix(pth, "/inputTokens"):
					p.inTok += int64(n)
					continue
				case strings.HasSuffix(pth, "/outputTokens"):
					p.outTok += int64(n)
					continue
				}
			}
		}
		// 3) model surfacing
		if o == "a" && strings.HasSuffix(pth, "/model") {
			continue
		}
		// 4) incremental content patches
		if strings.Contains(pth, "/content") {
			s, ok := v.(string)
			if !ok {
				continue
			}
			et := p.classifyContentPath(pth)
			switch et {
			case "tool_use":
				continue
			case "thinking":
				if o == "x" {
					if err := p.emitTyped(ctx, out, "thinking", s); err != nil {
						return false, err
					}
				}
			default:
				if o == "x" {
					if err := p.emitTyped(ctx, out, "text", s); err != nil {
						return false, err
					}
				} else if o == "p" {
					if err := p.emitTyped(ctx, out, "text", s); err != nil {
						return false, err
					}
				}
			}
		}
	}
	return false, nil
}

func (p *streamParser) classifyContentPath(path string) string {
	idx := strings.LastIndex(path, "/content")
	if idx < 0 {
		return "text"
	}
	return p.valueTypes[path[:idx]]
}

// absorbInlineSection registers inline value entries of a new section and
// captures their content immediately.
func (p *streamParser) absorbInlineSection(sectionIdx int, section map[string]any, ctx context.Context, out chan<- Event) {
	if p.valueCounts == nil {
		p.valueCounts = map[string]int{}
	}
	st, _ := section["type"].(string)
	values, ok := section["value"].([]any)
	if !ok {
		p.valueCounts[sectionKey(sectionIdx)] = 0
		return
	}
	if st != "agent-inference" && st != "agent-reply" && st != "assistant-reply" {
		return
	}
	prefix := sectionKey(sectionIdx)
	for i, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		et, _ := entry["type"].(string)
		if et != "" {
			if p.valueTypes == nil {
				p.valueTypes = map[string]string{}
			}
			p.valueTypes[fmt.Sprintf("%s/value/%d", prefix, i)] = et
		}
		if content, ok := entry["content"].(string); ok && content != "" {
			if err := p.emitTyped(ctx, out, et, content); err != nil {
				return
			}
		}
	}
	p.valueCounts[prefix] = len(values)
}

// emitTyped cleans Notion markup and routes text/thinking to events.
func (p *streamParser) emitTyped(ctx context.Context, out chan<- Event, entryType, content string) error {
	content = cleanNotionMarkup(content)
	if content == "" {
		return nil
	}
	// Mirror the validated reference: only text/thinking entries carry user
	// visible content; tool_use and unknown types are ignored.
	switch entryType {
	case "text":
		p.emittedText = true
		return send(ctx, out, Event{Kind: EventText, Text: content})
	case "thinking":
		return send(ctx, out, Event{Kind: EventReasoning, Text: content})
	}
	return nil
}

// handleAgentInference covers asPatchResponse=false cumulative events.
func (p *streamParser) handleAgentInference(ctx context.Context, top map[string]any, out chan<- Event) {
	values, _ := top["value"].([]any)
	var text, thinking strings.Builder
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		et, _ := entry["type"].(string)
		content, _ := entry["content"].(string)
		if content == "" {
			continue
		}
		switch et {
		case "text":
			text.WriteString(content)
		case "thinking":
			thinking.WriteString(content)
		}
	}
	full := text.String()
	if full != p.lastText {
		if delta := diffSuffix(p.lastText, full); delta != "" {
			p.emittedText = true
			_ = send(ctx, out, Event{Kind: EventText, Text: delta})
		}
		p.lastText = full
	}
	fullT := thinking.String()
	if fullT != p.lastThinking {
		if delta := diffSuffix(p.lastThinking, fullT); delta != "" {
			_ = send(ctx, out, Event{Kind: EventReasoning, Text: delta})
		}
		p.lastThinking = fullT
	}
}

// diffSuffix returns the part of full that extends prev.
func diffSuffix(prev, full string) string {
	if full == "" {
		return ""
	}
	if prev == "" {
		return full
	}
	if strings.HasPrefix(full, prev) {
		return full[len(prev):]
	}
	return ""
}

// emitFinal reports accumulated real usage tokens after the stream ends.
func (p *streamParser) emitFinal(out chan<- Event, ctx context.Context) {
	if p.inTok == 0 && p.outTok == 0 {
		return
	}
	select {
	case out <- Event{Kind: EventUsage, TokensIn: p.inTok, TokensOut: p.outTok}:
	case <-ctx.Done():
	}
}

// ---- Notion markup cleanup ----

var (
	reLangFull   = regexp.MustCompile(`(?s)<lang\b[^>]*>(.*?)</lang>`)
	reLangOpen   = regexp.MustCompile(`<lang\b[^>]*>`)
	reLangClose  = regexp.MustCompile(`</lang>`)
	rePrimaryArg = regexp.MustCompile(`\bprimary="[a-zA-Z\-]{1,15}"\s*`)
)

// cleanNotionMarkup strips internal <lang>/primary fragments that leak into
// streamed text chunks.
func cleanNotionMarkup(s string) string {
	s = reLangFull.ReplaceAllString(s, "$1")
	s = reLangClose.ReplaceAllString(s, "")
	s = reLangOpen.ReplaceAllString(s, "")
	s = rePrimaryArg.ReplaceAllString(s, "")
	return s
}

// ---- transcript block builders ----

var transcriptClock = func() string {
	return time.Now().Format("2006-01-02T15:04:05.000Z07:00")
}

// ConfigBlock builds the transcript config entry.
//
// toolsJSON — спеки тулзов для config-плейсмента (уже JSON-массив).
// Кладётся под ключом "tools": спеки выглядят харнесс-нативно, а не как
// "поддельные схемы в сообщении" — так у модели меньше повода отказываться
// от вызова (живые отказы Sonnet/Gemini 2026-09-12 ссылались именно на это).
func ConfigBlock(notionModel string, toolsJSON ...string) TranscriptEntry {
	cfg := map[string]any{
		"type":                                           "workflow",
		"modelFromUser":                                  true,
		"enableAgentAutomations":                         true,
		"enableAgentIntegrations":                        true,
		"enableCustomAgents":                             true,
		"enableExperimentalIntegrations":                 false,
		"enableAgentDiffs":                               true,
		"enableAgentUpdatePagePatch":                     true,
		"enableCsvAttachmentSupport":                     true,
		"enableDatabaseAgents":                           true,
		"showDatabaseAgentsDiscoverability":              true,
		"enableAgentThreadTools":                         false,
		"enableCrdtOperations":                           false,
		"enableAgentCardCustomization":                   true,
		"enableSystemPromptAsPage":                       false,
		"enableUserSessionContext":                       false,
		"enableLargeToolResultComputerOffload":           false,
		"enableScriptAgentAdvanced":                      false,
		"enableScriptAgent":                              true,
		"enableScriptAgentSearchConnectorsInCustomAgent": false,
		"enableScriptAgentGoogleDriveInCustomAgent":      false,
		"enableScriptAgentGoogleDriveOAuthInCustomAgent": false,
		"enableScriptAgentSlack":                         true,
		"enableScriptAgentMcpServers":                    false,
		"enableScriptAgentMail":                          true,
		"enableScriptAgentGtm":                           false,
		"enableScriptAgentCustomToolCalling":             true,
		"enableComputer":                                 false,
		"enableCreateAndRunThread":                       true,
		"enableSoftwareFactoryPage":                      false,
		"enableAgentGenerateImage":                       true,
		"enableSpeculativeSearch":                        false,
		"enableQueryCalendar":                            false,
		"enableQueryMail":                                false,
		"enableMailExplicitToolCalls":                    true,
		"enableMailNotificationPreferences":              false,
		"enableMailAgentMultiProviderSupport":            false,
		"useRulePrioritization":                          true,
		"availableConnectors":                            []any{},
		"customConnectorInfo":                            []any{},
		"searchScopes":                                   []any{map[string]any{"type": "everything"}},
		"useSearchToolV2":                                false,
		"useWebSearch":                                   true,
		"isHipaa":                                        false,
		"yoloMode":                                       false,
		"useReadOnlyMode":                                false,
		"writerMode":                                     false,
		"isCustomAgent":                                  false,
		"isCustomAgentBuilder":                           false,
		"isAgentResearchRequest":                         false,
		"useCustomAgentDraft":                            false,
		"use_draft_actor_pointer":                        false,
		"enableUpdatePageAutofixer":                      true,
		"enableMarkdownVNext":                            false,
		"updatePageStaleViewGuardEnabled":                false,
		"enableUpdatePageOrderUpdates":                   true,
		"enableAgentSupportPropertyReorder":              true,
		"agentShortUpdatePageResult":                     true,
		"enableAgentAskSurvey":                           true,
		"databaseAgentConfigMode":                        false,
		"isOnboardingAgent":                              false,
		"isMobile":                                       false,
	}
	if notionModel != "" {
		cfg["model"] = notionModel
	}
	if len(toolsJSON) > 0 && strings.TrimSpace(toolsJSON[0]) != "" {
		var specs any
		if json.Unmarshal([]byte(toolsJSON[0]), &specs) == nil {
			cfg["tools"] = specs
			cfg["toolChoice"] = "auto"
		}
	}
	return TranscriptEntry{ID: model.NewID(), Type: "config", Value: cfg}
}

// ContextBlock builds the transcript context entry.
func ContextBlock(userID, email, userName, spaceID, spaceName, spaceViewID string) TranscriptEntry {
	return TranscriptEntry{ID: model.NewID(), Type: "context", Value: map[string]any{
		"timezone":        "UTC",
		"userName":        userName,
		"userId":          userID,
		"userEmail":       email,
		"spaceName":       spaceName,
		"spaceId":         spaceID,
		"spaceViewId":     spaceViewID,
		"currentDatetime": transcriptClock(),
		"surface":         "ai_module",
	}}
}

// UserBlock builds a user message entry.
// userId штампуется позже центрально в RunInferenceStream (из аккаунта),
// руками его ставить не нужно.
func UserBlock(text string) TranscriptEntry {
	return TranscriptEntry{
		ID:        model.NewID(),
		Type:      "user",
		Value:     [][]string{{text}},
		CreatedAt: transcriptClock(),
	}
}

// UserSpecifiedContextBlock builds the empty user-specified-context entry.
//
// Настоящий веб-клиент всегда шлёт этот блок между context и первым
// сообщением (подтверждено HAR-капчей notion-forge):
// {"type":"user-specified-context","value":{"pointers":[...]}}.
// Сюда Notion кладёт указатели на упомянутые @-страницы; без блока та же
// семантика — пустой список.
func UserSpecifiedContextBlock() TranscriptEntry {
	return TranscriptEntry{ID: model.NewID(), Type: "user-specified-context", Value: map[string]any{
		"pointers": []any{},
	}}
}

// AssistantBlock builds an assistant (agent-inference) message entry.
func AssistantBlock(text string) TranscriptEntry {
	return TranscriptEntry{
		ID:    model.NewID(),
		Type:  "agent-inference",
		Value: []TranscriptContent{{Type: "text", Content: text}},
	}
}

// FullText joins all text events seen for a stream.
func FullText(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == EventText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

// ReasoningText joins all reasoning events.
func ReasoningText(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == EventReasoning {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

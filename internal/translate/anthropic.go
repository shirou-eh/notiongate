package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/tools"
)

// ---- Anthropic request ----

type anthropicContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_result / tool_use payloads are ignored in v1.
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model      string             `json:"model"`
	System     json.RawMessage    `json:"system"`
	Messages   []anthropicMessage `json:"messages"`
	Stream     bool               `json:"stream"`
	MaxTokens  int                `json:"max_tokens"`
	Metadata   *anthropicMetadata `json:"metadata,omitempty"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice any                `json:"tool_choice,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// ParseAnthropic converts an Anthropic /v1/messages body into a ChatJob.
func ParseAnthropic(body []byte) (*ChatJob, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	job := &ChatJob{
		Model:    req.Model,
		UserKey:  userIDFromMeta(req),
		Protocol: "anthropic",
		Stream:   req.Stream,
	}
	for _, t := range req.Tools {
		if t.Name == "" {
			continue
		}
		job.Tools = append(job.Tools, Tool{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}
	if req.ToolChoice != nil {
		switch v := req.ToolChoice.(type) {
		case map[string]any:
			if typ, ok := v["type"].(string); ok {
				job.ToolChoice = typ
			}
		case string:
			job.ToolChoice = v
		}
	} else if len(job.Tools) > 0 {
		job.ToolChoice = "auto"
	}
	if sys := extractAnthropicSystem(req.System); sys != "" {
		job.System = append(job.System, sys)
	}
	for _, m := range req.Messages {
		text, files := extractAnthropicText(m.Content)
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		// Handle tool_use blocks that were not in content but as separate tool objects
		// They are already handled via text placeholder above
		job.Turns = append(job.Turns, Turn{Role: role, Text: text, Files: files})
	}
	return job, nil
}

func userIDFromMeta(req anthropicRequest) string {
	if req.Metadata != nil {
		return req.Metadata.UserID
	}
	return ""
}

func extractAnthropicSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []anthropicContentPart
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteString("\n\n")
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func extractAnthropicText(raw json.RawMessage) (string, []FileAttachment) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		var files []FileAttachment
		for _, p := range parts {
			t, _ := p["type"].(string)
			switch t {
			case "text":
				if txt, ok := p["text"].(string); ok {
					b.WriteString(txt)
				}
			case "image":
				src, _ := p["source"].(map[string]any)
				if src != nil {
					if data, ok := src["data"].(string); ok {
						files = append(files, FileAttachment{URL: "data:image/jpeg;base64," + data, ContentType: "image/jpeg"})
					} else if url, ok := src["url"].(string); ok {
						files = append(files, FileAttachment{URL: url, ContentType: "image/*"})
					}
				}
			case "document":
				src, _ := p["source"].(map[string]any)
				if src != nil {
					if data, ok := src["data"].(string); ok {
						files = append(files, FileAttachment{URL: "data:application/pdf;base64," + data, ContentType: "application/pdf"})
					}
				}
			case "tool_result":
				if content, ok := p["content"].(string); ok {
					b.WriteString("[tool_result] " + content)
				} else if content, ok := p["content"].([]any); ok {
					for _, c := range content {
						if m, ok := c.(map[string]any); ok {
							if txt, ok := m["text"].(string); ok {
								b.WriteString(txt)
							}
						}
					}
				}
			case "tool_use":
				if name, ok := p["name"].(string); ok {
					b.WriteString(fmt.Sprintf("[tool_use %s]", name))
				}
			}
		}
		return b.String(), files
	}
	return "", nil
}

func extractAnthropicTextLegacy(raw json.RawMessage) string {
	s, _ := extractAnthropicText(raw)
	return s
}

// ---- Anthropic response rendering ----

type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type AnthropicBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type anthropicResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []AnthropicBlock `json:"content"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        anthropicUsage   `json:"usage"`
}

// BuildAnthropicResponse renders a complete (non-streaming) message.
func BuildAnthropicResponse(id, mdl string, events []notion.Event, inTok, outTok int64) []byte {
	stop := "end_turn"
	resp := anthropicResponse{
		ID:         id,
		Type:       "message",
		Role:       "assistant",
		Model:      mdl,
		StopReason: &stop,
		Usage:      anthropicUsage{InputTokens: inTok, OutputTokens: outTok},
	}
	if r := notion.ReasoningText(events); r != "" {
		resp.Content = append(resp.Content, AnthropicBlock{Type: "thinking", Thinking: r})
	}
	resp.Content = append(resp.Content, AnthropicBlock{Type: "text", Text: notion.FullText(events)})
	b, _ := json.Marshal(resp)
	return b
}

func BuildAnthropicResponseWithTools(id, mdl string, calls []tools.Call, cleanText string, inTok, outTok int64) []byte {
	m := map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"model":       mdl,
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": inTok, "output_tokens": outTok},
	}
	var content []any
	if cleanText != "" {
		content = append(content, map[string]any{"type": "text", "text": cleanText})
	}
	for _, c := range calls {
		var input any
		_ = json.Unmarshal([]byte(c.Arguments), &input)
		if input == nil {
			input = map[string]any{}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": input})
	}
	m["content"] = content
	bb, _ := json.Marshal(m)
	return bb
}

// Anthropic SSE event builders (payloads only; the api layer writes the
// "event:"/"data:" framing).

type anthropicSSEMessage struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Model      string           `json:"model"`
	Content    []AnthropicBlock `json:"content"`
	StopReason *string          `json:"stop_reason"`
	Usage      anthropicUsage   `json:"usage"`
}

func BuildAnthropicMessageStart(id, mdl string, inTok int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "message_start",
		"message": anthropicSSEMessage{
			ID: id, Type: "message", Role: "assistant", Model: mdl,
			Content: []AnthropicBlock{},
			Usage:   anthropicUsage{InputTokens: inTok, OutputTokens: 0},
		},
	})
	return b
}

func BuildAnthropicBlockStart(index int, block AnthropicBlock) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "content_block_start", "index": index, "content_block": block,
	})
	return b
}

func BuildAnthropicTextDelta(index int, text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": index,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	return b
}

func BuildAnthropicThinkingDelta(index int, thinking string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
	})
	return b
}

func BuildAnthropicBlockStop(index int) []byte {
	b, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": index})
	return b
}

func BuildAnthropicMessageDelta(outTok int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outTok},
	})
	return b
}

func BuildAnthropicMessageStop() []byte {
	b, _ := json.Marshal(map[string]any{"type": "message_stop"})
	return b
}

func BuildAnthropicPing() []byte {
	b, _ := json.Marshal(map[string]any{"type": "ping"})
	return b
}

// BuildAnthropicError renders an Anthropic-style error body.
func BuildAnthropicError(errType, message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	return b
}

// BuildAnthropicModels renders GET /v1/models (Anthropic flavour).
func BuildAnthropicModels(ids []string) []byte {
	type m struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	out := struct {
		Data    []m    `json:"data"`
		FirstID string `json:"first_id,omitempty"`
		HasMore bool   `json:"has_more"`
	}{HasMore: false}
	for _, id := range ids {
		out.Data = append(out.Data, m{Type: "model", ID: id})
	}
	if len(out.Data) > 0 {
		out.FirstID = out.Data[0].ID
	}
	if out.Data == nil {
		out.Data = []m{}
	}
	b, _ := json.Marshal(out)
	return b
}

// NewMessageID generates an Anthropic-style message id.
func NewMessageID() string { return "msg_" + model.NewID() }

package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/tools"
)

// ---- OpenAI request ----

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Stream        bool            `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	User       string       `json:"user"`
	Tools      []openAITool `json:"tools,omitempty"`
	ToolChoice any          `json:"tool_choice,omitempty"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

// ParseOpenAI converts an OpenAI /v1/chat/completions body into a ChatJob.
// Only text content is forwarded in v1; image parts and tool payloads are
// ignored (documented limitation).
func ParseOpenAI(body []byte) (*ChatJob, error) {
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	job := &ChatJob{
		Model:    req.Model,
		UserKey:  req.User,
		Protocol: "openai",
		Stream:   req.Stream,
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	if includeUsage {
		job.StreamUsage = true
	}
	// tools
	for _, t := range req.Tools {
		if t.Function.Name == "" {
			continue
		}
		job.Tools = append(job.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	if req.ToolChoice != nil {
		switch v := req.ToolChoice.(type) {
		case string:
			job.ToolChoice = v
		case map[string]any:
			if typ, ok := v["type"].(string); ok {
				job.ToolChoice = typ
			} else if fn, ok := v["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok && n != "" {
					job.ToolChoice = "required"
				}
			}
		}
	} else if len(job.Tools) > 0 {
		job.ToolChoice = "auto"
	}
	for _, m := range req.Messages {
		text, files := extractOpenAITextAndFiles(m.Content)
		switch m.Role {
		case "system", "developer":
			job.System = append(job.System, text)
		case "assistant":
			// Assistant с tool_calls (агентный луп: OpenCode/Claude Code шлют
			// content=null + tool_calls=[{id, function:{name, arguments}}]).
			// Сохраняем вызовы текстом с ID чтобы transcript не терял связь
			// "вызов → результат" на следующем шаге.
			if len(m.ToolCalls) > 0 {
				var b strings.Builder
				if strings.TrimSpace(text) != "" {
					b.WriteString(strings.TrimSpace(text))
					b.WriteString("\n")
				}
				for _, tc := range m.ToolCalls {
					args := strings.TrimSpace(tc.Function.Arguments)
					if args == "" {
						args = "{}"
					}
					fmt.Fprintf(&b, "[assistant tool_call id=%s name=%s args=%s]\n",
						tc.ID, tc.Function.Name, args)
				}
				job.Turns = append(job.Turns, Turn{Role: "assistant", Text: strings.TrimSpace(b.String()), Files: files})
			} else {
				// legacy: некоторые клиенты кладут tool_calls внутрь content
				var aux struct {
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				}
				if json.Unmarshal(m.Content, &aux) == nil && len(aux.ToolCalls) > 0 {
					// Tool call — keep as assistant text for context, tools will be extracted
					job.Turns = append(job.Turns, Turn{Role: "assistant", Text: text, Files: files})
				} else {
					job.Turns = append(job.Turns, Turn{Role: "assistant", Text: text, Files: files})
				}
			}
		case "tool", "function":
			// Tool result — сохраняем tool_call_id чтобы модель видела
			// какой вызов отработал. Файлы правит агент на компе
			// пользователя, прокси только честно возит результаты.
			toolName := m.Name
			prefix := "[tool output]"
			if m.ToolCallID != "" && toolName != "" {
				prefix = fmt.Sprintf("[tool %s result id=%s]", toolName, m.ToolCallID)
			} else if m.ToolCallID != "" {
				prefix = fmt.Sprintf("[tool result id=%s]", m.ToolCallID)
			} else if toolName != "" {
				prefix = fmt.Sprintf("[tool %s result]", toolName)
			}
			if strings.TrimSpace(text) == "" {
				text = "{}"
			}
			text = prefix + " " + text
			job.Turns = append(job.Turns, Turn{Role: "user", Text: text, Files: files})
		default:
			job.Turns = append(job.Turns, Turn{Role: "user", Text: text, Files: files})
		}
	}
	return job, nil
}

func extractOpenAIText(raw json.RawMessage) string {
	t, _ := extractOpenAITextAndFiles(raw)
	return t
}

func extractOpenAITextAndFiles(raw json.RawMessage) (string, []FileAttachment) {
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
				} else if txtObj, ok := p["text"].(map[string]any); ok {
					if v, ok := txtObj["value"].(string); ok {
						b.WriteString(v)
					}
				}
			case "image_url":
				if img, ok := p["image_url"].(map[string]any); ok {
					if url, ok := img["url"].(string); ok {
						files = append(files, FileAttachment{URL: url, ContentType: "image/*"})
					}
				}
			case "image":
				if url, ok := p["image"].(string); ok {
					files = append(files, FileAttachment{URL: url, ContentType: "image/*"})
				}
			case "file", "document":
				if f, ok := p["file"].(map[string]any); ok {
					if url, ok := f["url"].(string); ok {
						files = append(files, FileAttachment{URL: url, ContentType: "application/pdf"})
					}
				}
			}
		}
		return b.String(), files
	}
	return "", nil
}

// ---- OpenAI response rendering ----

type openAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type openAIMessageOut struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChoice struct {
	Index        int               `json:"index"`
	Message      *openAIMessageOut `json:"message,omitempty"`
	Delta        *openAIMessageOut `json:"delta,omitempty"`
	FinishReason *string           `json:"finish_reason"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

// BuildOpenAIResponse renders a complete (non-streaming) chat.completion.
func BuildOpenAIResponse(id, mdl string, events []notion.Event, inTok, outTok int64) []byte {
	content := notion.FullText(events)
	return buildOpenAIResponseWithContent(id, mdl, &content, notion.ReasoningText(events), nil, "stop", inTok, outTok)
}

func buildOpenAIResponseWithContent(id, mdl string, content *string, reasoning string, toolCalls []openAIToolCall, finish string, inTok, outTok int64) []byte {
	resp := openAIResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   mdl,
		Choices: []openAIChoice{{
			Index:   0,
			Message: &openAIMessageOut{Role: "assistant", Content: content, ReasoningContent: reasoning, ToolCalls: toolCalls},
		}},
		Usage: &openAIUsage{PromptTokens: inTok, CompletionTokens: outTok, TotalTokens: inTok + outTok},
	}
	resp.Choices[0].FinishReason = &finish
	b, _ := json.Marshal(resp)
	return b
}

// BuildOpenAIResponseWithTools renders a chat.completion that contains tool_calls.
func BuildOpenAIResponseWithTools(id, mdl string, calls []tools.Call, cleanText, reasoning string, inTok, outTok int64) []byte {
	var toolCalls []openAIToolCall
	for _, c := range calls {
		toolCalls = append(toolCalls, openAIToolCall{ID: c.ID, Type: "function", Function: openAIToolCallFunc{Name: c.Name, Arguments: c.Arguments}})
	}
	var content *string
	if cleanText != "" {
		content = &cleanText
	}
	// reasoning stays as reasoning_content, not mixed with tool_calls
	return buildOpenAIResponseWithContent(id, mdl, content, reasoning, toolCalls, "tool_calls", inTok, outTok)
}

// BuildOpenAIChunk renders one chat.completion.chunk SSE payload.
// finish == nil marks an intermediate chunk; role is set only on the first.
func BuildOpenAIChunk(id, mdl string, first bool, contentDelta, reasoningDelta string, finish *string) []byte {
	delta := &openAIMessageOut{}
	if first {
		delta.Role = "assistant"
	}
	if contentDelta != "" {
		delta.Content = &contentDelta
	}
	delta.ReasoningContent = reasoningDelta
	if !first && contentDelta == "" && reasoningDelta == "" && finish == nil {
		delta = &openAIMessageOut{}
	}
	resp := openAIResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   mdl,
		Choices: []openAIChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
	b, _ := json.Marshal(resp)
	return b
}

// BuildOpenAIToolChunk renders one streaming tool_calls delta.
// firstCall=true включает id+name (первый чанк по каждому tool index),
// чтобы агенты (OpenCode/Claude Code) собрали вызов целиком.
// Аргументы шлём целиком — клиенты умеют склеивать delta.function.arguments.
func BuildOpenAIToolChunk(id, mdl string, index int, callID, name, args string, firstCall bool) []byte {
	fn := map[string]any{"arguments": args}
	if firstCall {
		fn["name"] = name
	}
	tc := map[string]any{"index": index, "type": "function", "function": fn}
	if firstCall && callID != "" {
		tc["id"] = callID
	}
	delta := map[string]any{"role": "assistant", "tool_calls": []any{tc}}
	// У первого вызова role уже был в начальном чанке — дублировать ок,
	// клиенты игнорируют повторный role.
	resp := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model":   mdl,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	}
	b, _ := json.Marshal(resp)
	return b
}

// BuildOpenAIError renders an OpenAI-style error body.
func BuildOpenAIError(message, errType, code string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": errType, "code": code},
	})
	return b
}

// BuildOpenAIModels renders GET /v1/models.
func BuildOpenAIModels(ids []string) []byte {
	type m struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string `json:"object"`
		Data   []m    `json:"data"`
	}{Object: "list"}
	created := time.Now().Unix()
	for _, id := range ids {
		out.Data = append(out.Data, m{ID: id, Object: "model", Created: created, OwnedBy: "notiongate"})
	}
	if out.Data == nil {
		out.Data = []m{}
	}
	b, _ := json.Marshal(out)
	return b
}

// NewCompletionID generates an OpenAI-style completion id.
func NewCompletionID() string { return "chatcmpl-" + model.NewID() }

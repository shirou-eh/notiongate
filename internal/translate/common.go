// Package translate converts between client-facing protocols (OpenAI,
// Anthropic) and the Notion transcript representation.
package translate

import (
	"strings"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/notion"
)

// FileAttachment represents an image/pdf/document attached to a turn.
type FileAttachment struct {
	URL         string `json:"url"`
	Data        []byte `json:"-"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// Turn is one normalized chat message.
type Turn struct {
	Role  string // "user" | "assistant"
	Text  string
	Files []FileAttachment
}

// Tool — normalized tool definition (OpenAI function / Anthropic tool).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"` // JSON Schema
}

type ChatJob struct {
	Model    string // client-facing model name
	UserKey  string // sticky session key
	UserID   string // Notion-side user id (stamped onto user transcript blocks)
	Protocol string // "openai" | "anthropic"
	Stream   bool
	// StreamUsage: OpenAI stream_options.include_usage — emit a final usage
	// chunk in streaming mode.
	StreamUsage bool
	System      []string
	Turns       []Turn
	Tools       []Tool
	ToolChoice  string // "auto" | "required" | "none" | ""
}

// EstimateTokens gives a rough token count (~4 chars/token). Used only when
// Notion did not report real usage tokens for a stream.
func EstimateTokens(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(len(s) / 4)
	if n == 0 {
		n = 1
	}
	return n
}

// BuildTranscript converts a ChatJob into Notion transcript message blocks
// (user / agent-inference):
//   - system prompts are merged and prepended to the first user message;
//   - consecutive same-role turns are merged;
//   - config/context blocks are prepended later by the notion client.
func BuildTranscript(job *ChatJob) []notion.TranscriptEntry {
	turns := make([]Turn, len(job.Turns))
	copy(turns, job.Turns)

	if sys := strings.Join(job.System, "\n\n"); sys != "" {
		sysTurn := Turn{Role: "user", Text: "Instructions:\n" + sys}
		if len(turns) > 0 && turns[0].Role == "user" {
			turns[0] = Turn{Role: "user", Text: sysTurn.Text + "\n\n" + turns[0].Text}
		} else {
			turns = append([]Turn{sysTurn}, turns...)
		}
	}

	// Merge consecutive same-role turns in O(n): accumulate per-run with a
	// strings.Builder instead of quadratic re-copying (a hostile client with
	// thousands of messages used to burn seconds of CPU per request).
	type run struct {
		assistant bool
		texts     []string
		files     []FileAttachment
	}
	var runs []run
	for _, t := range turns {
		trimmed := strings.TrimSpace(t.Text)
		hasFiles := len(t.Files) > 0
		if trimmed == "" && !hasFiles {
			continue // whitespace-only messages carry nothing to infer
		}
		assistant := t.Role == "assistant"
		if len(runs) == 0 || runs[len(runs)-1].assistant != assistant {
			runs = append(runs, run{assistant: assistant})
		}
		if trimmed != "" {
			runs[len(runs)-1].texts = append(runs[len(runs)-1].texts, trimmed)
		}
		if hasFiles {
			runs[len(runs)-1].files = append(runs[len(runs)-1].files, t.Files...)
		}
	}

	var out []notion.TranscriptEntry
	for _, r := range runs {
		var b strings.Builder
		for i, txt := range r.texts {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(txt)
		}
		text := b.String()
		if r.assistant {
			if text != "" {
				out = append(out, notion.AssistantBlock(text))
			}
			// Assistant files are not expected, but handle
			for _, f := range r.files {
				out = append(out, notion.TranscriptEntry{
					ID:   model.NewID(),
					Type: "file",
					Value: map[string]any{
						"url":          f.URL,
						"filename":     f.Filename,
						"content_type": f.ContentType,
					},
				})
			}
		} else {
			if text != "" {
				e := notion.UserBlock(text)
				e.UserID = job.UserID // "" ок — RunInferenceStream доштампует из аккаунта
				out = append(out, e)
			}
			// Files as separate blocks after text (Notion file blocks)
			for _, f := range r.files {
				out = append(out, notion.TranscriptEntry{
					ID:   model.NewID(),
					Type: "file",
					Value: map[string]any{
						"url":          f.URL,
						"filename":     f.Filename,
						"content_type": f.ContentType,
					},
				})
			}
		}
	}
	return out
}

// TranscriptInputTokens estimates the token count of a transcript.
func TranscriptInputTokens(t []notion.TranscriptEntry) int64 {
	var total int64
	for _, e := range t {
		switch v := e.Value.(type) {
		case [][]string:
			for _, inner := range v {
				for _, s := range inner {
					total += EstimateTokens(s)
				}
			}
		case []notion.TranscriptContent:
			for _, c := range v {
				total += EstimateTokens(c.Content)
			}
		}
	}
	return total
}

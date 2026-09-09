package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shirou-eh/notiongate/internal/notion"
)

func TestParseOpenAI(t *testing.T) {
	body := []byte(`{
		"model": "sonnet-4.6",
		"stream": true,
		"user": "user-42",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
			{"role": "user", "content": [
				{"type": "text", "text": "look "},
				{"type": "image_url", "image_url": {"url": "http://x/y.png"}}
			]},
			{"role": "tool", "content": "42"}
		]
	}`)
	job, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if job.Model != "sonnet-4.6" || job.UserKey != "user-42" || !job.Stream || job.Protocol != "openai" {
		t.Fatalf("job = %+v", job)
	}
	if strings.Join(job.System, "|") != "be brief" {
		t.Fatalf("system = %v", job.System)
	}
	if len(job.Turns) != 4 {
		t.Fatalf("turns = %+v", job.Turns)
	}
	if job.Turns[2].Text != "look " {
		t.Fatalf("array content text = %q (image part must be ignored)", job.Turns[2].Text)
	}
	if !strings.HasPrefix(job.Turns[3].Text, "[tool output] ") {
		t.Fatalf("tool turn = %q", job.Turns[3].Text)
	}
}

func TestParseOpenAIErrors(t *testing.T) {
	if _, err := ParseOpenAI([]byte(`not json`)); err == nil {
		t.Fatal("invalid json must fail")
	}
	if _, err := ParseOpenAI([]byte(`{"messages":[]}`)); err == nil {
		t.Fatal("empty messages must fail")
	}
}

func TestBuildTranscript(t *testing.T) {
	job := &ChatJob{
		System: []string{"sys1", "sys2"},
		Turns: []Turn{
			{Role: "user", Text: "q1"},
			{Role: "user", Text: "q2"},
			{Role: "assistant", Text: "a1"},
			{Role: "user", Text: "q3"},
		},
	}
	tr := BuildTranscript(job)
	if len(tr) != 3 {
		t.Fatalf("transcript = %+v", tr)
	}
	userText := func(e notion.TranscriptEntry) string {
		arr, _ := e.Value.([][]string)
		if len(arr) == 0 || len(arr[0]) == 0 {
			return ""
		}
		return arr[0][0]
	}
	if !strings.HasPrefix(userText(tr[0]), "Instructions:\nsys1\n\nsys2\n\nq1") {
		t.Fatalf("system prefix wrong: %q", userText(tr[0]))
	}
	if !strings.HasSuffix(userText(tr[0]), "q1\n\nq2") {
		t.Fatalf("merge wrong: %q", userText(tr[0]))
	}
	if tr[1].Type != "agent-inference" || tr[2].Type != "user" {
		t.Fatalf("types wrong: %+v", tr)
	}
}

func TestBuildTranscriptSystemOnly(t *testing.T) {
	tr := BuildTranscript(&ChatJob{System: []string{"only sys"}})
	if len(tr) != 1 || tr[0].Type != "user" {
		t.Fatalf("transcript = %+v", tr)
	}
}

func TestParseAnthropic(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"max_tokens": 100,
		"stream": false,
		"system": [{"type": "text", "text": "be nice"}],
		"metadata": {"user_id": "u-9"},
		"messages": [
			{"role": "user", "content": "q1"},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`)
	job, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if job.Model != "claude-x" || job.UserKey != "u-9" || job.Stream {
		t.Fatalf("job = %+v", job)
	}
	if strings.Join(job.System, "|") != "be nice" {
		t.Fatalf("system = %v", job.System)
	}
	if len(job.Turns) != 3 || job.Turns[0].Text != "q1" || job.Turns[1].Text != "a1" {
		t.Fatalf("turns = %+v", job.Turns)
	}
}

func TestBuildOpenAIResponse(t *testing.T) {
	events := []notion.Event{
		{Kind: notion.EventText, Text: "Hello"},
		{Kind: notion.EventReasoning, Text: "hmm"},
		{Kind: notion.EventText, Text: " world"},
	}
	b := BuildOpenAIResponse("chatcmpl-1", "m", events, 12, 5)
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
		Object string `json:"object"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "chat.completion" || resp.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("resp = %s", b)
	}
	if resp.Choices[0].Message.ReasoningContent != "hmm" {
		t.Fatalf("reasoning = %q", resp.Choices[0].Message.ReasoningContent)
	}
	if resp.Usage.TotalTokens != 17 {
		t.Fatalf("usage = %d", resp.Usage.TotalTokens)
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "stop" {
		t.Fatal("finish_reason stop required")
	}
}

func TestBuildOpenAIChunks(t *testing.T) {
	first := BuildOpenAIChunk("id", "m", true, "", "", nil)
	if !strings.Contains(string(first), `"role":"assistant"`) {
		t.Fatalf("first chunk must carry role: %s", first)
	}
	mid := BuildOpenAIChunk("id", "m", false, "x", "", nil)
	if !strings.Contains(string(mid), `"content":"x"`) {
		t.Fatalf("mid chunk: %s", mid)
	}
	stop := "stop"
	last := BuildOpenAIChunk("id", "m", false, "", "", &stop)
	if !strings.Contains(string(last), `"finish_reason":"stop"`) {
		t.Fatalf("last chunk: %s", last)
	}
}

func TestBuildAnthropicResponse(t *testing.T) {
	events := []notion.Event{
		{Kind: notion.EventReasoning, Text: "think"},
		{Kind: notion.EventText, Text: "answer"},
	}
	b := BuildAnthropicResponse("msg_1", "m", events, 3, 4)
	var resp struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != "message" || resp.Role != "assistant" || len(resp.Content) != 2 {
		t.Fatalf("resp = %s", b)
	}
	if resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "think" {
		t.Fatalf("thinking block = %+v", resp.Content[0])
	}
	if resp.Content[1].Type != "text" || resp.Content[1].Text != "answer" {
		t.Fatalf("text block = %+v", resp.Content[1])
	}
}

func TestBuildAnthropicModels(t *testing.T) {
	b := BuildAnthropicModels([]string{"a", "b"})
	if !strings.Contains(string(b), `"id":"a"`) || !strings.Contains(string(b), `"first_id":"a"`) {
		t.Fatalf("models = %s", b)
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty must be 0")
	}
	if EstimateTokens("abcd") != 1 {
		t.Fatal("short must be 1")
	}
	if EstimateTokens("abcdefgh") != 2 {
		t.Fatal("8 chars = 2 tokens")
	}
}

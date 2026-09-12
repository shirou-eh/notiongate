package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseOpenAIToolCalls(t *testing.T) {
	// Агентный луп OpenAI: assistant с tool_calls + tool-результат с ID.
	body := []byte(`{
		"model": "m",
		"tools": [{"type":"function","function":{"name":"Edit","description":"edit file","parameters":{"type":"object"}}}],
		"messages": [
			{"role": "user", "content": "создай файл a.txt"},
			{"role": "assistant", "content": null, "tool_calls": [{"id":"call_1","type":"function","function":{"name":"Edit","arguments":"{\"path\":\"a.txt\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "name": "Edit", "content": "ok"}
		]
	}`)
	job, err := ParseOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Tools) != 1 || job.Tools[0].Name != "Edit" {
		t.Fatalf("tools = %+v", job.Tools)
	}
	if len(job.Turns) != 3 {
		t.Fatalf("turns = %+v", job.Turns)
	}
	// Assistant turn должен сохранить id+name чтобы не рвалась связка.
	if !strings.Contains(job.Turns[1].Text, "call_1") || !strings.Contains(job.Turns[1].Text, "Edit") {
		t.Fatalf("assistant tool_call lost: %q", job.Turns[1].Text)
	}
	// Tool turn должен сохранить id.
	if !strings.Contains(job.Turns[2].Text, "call_1") {
		t.Fatalf("tool result id lost: %q", job.Turns[2].Text)
	}
	tr := BuildTranscript(job)
	if len(tr) == 0 {
		t.Fatalf("empty transcript")
	}
	joined := ""
	for _, e := range tr {
		if arr, ok := e.Value.([][]string); ok {
			for _, inner := range arr {
				for _, s := range inner {
					joined += s + "\n"
				}
			}
		}
	}
	if !strings.Contains(joined, "call_1") {
		t.Fatalf("transcript lost tool id: %q", joined)
	}
}

func TestParseAnthropicToolBlocks(t *testing.T) {
	body := []byte(`{
		"model": "m", "max_tokens": 100,
		"tools": [{"name":"Bash","description":"run","input_schema":{"type":"object"}}],
		"messages": [
			{"role": "user", "content": "запусти ls"},
			{"role": "assistant", "content": [{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},
			{"role": "user", "content": [{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}
		]
	}`)
	job, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Tools) != 1 || job.Tools[0].Name != "Bash" {
		t.Fatalf("tools = %+v", job.Tools)
	}
	if len(job.Turns) != 3 {
		t.Fatalf("turns = %+v", job.Turns)
	}
	if !strings.Contains(job.Turns[1].Text, "toolu_1") || !strings.Contains(job.Turns[1].Text, "Bash") {
		t.Fatalf("tool_use lost: %q", job.Turns[1].Text)
	}
	if !strings.Contains(job.Turns[2].Text, "toolu_1") || !strings.Contains(job.Turns[2].Text, "a.txt") {
		t.Fatalf("tool_result lost: %q", job.Turns[2].Text)
	}
}

func TestBuildOpenAIToolChunk(t *testing.T) {
	b := BuildOpenAIToolChunk("id1", "m", 0, "call_1", "Edit", `{"path":"a"}`, true)
	var out struct {
		Choices []struct {
			Delta struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || len(out.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("chunk = %s", b)
	}
	tc := out.Choices[0].Delta.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "Edit" {
		t.Fatalf("tc = %+v", tc)
	}
}

func TestBuildAnthropicToolBlocks(t *testing.T) {
	s := BuildAnthropicToolStart(1, "toolu_1", "Bash")
	if !strings.Contains(string(s), "toolu_1") || !strings.Contains(string(s), "tool_use") {
		t.Fatalf("start = %s", s)
	}
	d := BuildAnthropicToolDelta(1, `{"command":"ls"}`)
	if !strings.Contains(string(d), "input_json_delta") || !strings.Contains(string(d), "ls") {
		t.Fatalf("delta = %s", d)
	}
}

func TestParseOpenAIEffort(t *testing.T) {
	mk := func(extra string) *ChatJob {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]` + extra + `}`)
		job, err := ParseOpenAI(body)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	if got := mk(``).Effort; got != "" {
		t.Fatalf("default effort = %q", got)
	}
	if got := mk(`,"reasoning_effort":"high"`).Effort; got != "high" {
		t.Fatalf("effort = %q", got)
	}
	if got := mk(`,"effort":"LOW"`).Effort; got != "low" {
		t.Fatalf("effort alias = %q", got)
	}
	if got := mk(`,"reasoning_effort":"ultra"`).Effort; got != "" {
		t.Fatalf("bad effort must drop: %q", got)
	}
	if got := mk(`,"tools_placement":"config"`).ToolsPlacement; got != "config" {
		t.Fatalf("placement = %q", got)
	}
	if got := mk(``).ToolsPlacement; got != "system" {
		t.Fatalf("default placement = %q", got)
	}
	for _, e := range []string{"low", "medium", "high"} {
		if EffortInstruction(e) == "" {
			t.Fatalf("no instruction for %s", e)
		}
	}
	spec := ToolsSpecJSON([]Tool{{Name: "Edit", Description: "d", Parameters: map[string]any{"type": "object"}}})
	if !strings.Contains(spec, `"name":"Edit"`) {
		t.Fatalf("spec = %s", spec)
	}
	if ToolsSpecJSON(nil) != "" {
		t.Fatalf("empty tools must give empty spec")
	}
}

func TestParseAnthropicEffort(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":10,"effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	job, err := ParseAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	if job.Effort != "high" {
		t.Fatalf("effort = %q", job.Effort)
	}
}

package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractAllowed_ClientToolsPassthrough(t *testing.T) {
	// Клиентские тулзы агента (Edit/Bash/Read) должны проходить,
	// даже если их нет в серверном реестре.
	text := "```json\n{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Edit\",\"arguments\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}}]}\n```"
	calls, _, ok := DefaultBridge.ExtractAllowed(text, []string{"Edit", "Bash"})
	if !ok || len(calls) != 1 {
		t.Fatalf("passthrough failed: ok=%v calls=%v", ok, calls)
	}
	if calls[0].Name != "Edit" || calls[0].ID != "call_1" {
		t.Fatalf("wrong call: %+v", calls[0])
	}
}

func TestExtractLegacy_UnknownDropped(t *testing.T) {
	// Старое поведение без allowlist: чужой тул отбрасывается.
	text := "```json\n{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Edit\",\"arguments\":\"{}\"}}]}\n```"
	if _, _, ok := DefaultBridge.Extract(text); ok {
		t.Fatalf("legacy Extract must drop unknown Edit")
	}
	// А встроенный тул проходит и так.
	text2 := "```json\n{\"tool_calls\":[{\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"x\\\"}\"}}]}\n```"
	if _, _, ok := DefaultBridge.Extract(text2); !ok {
		t.Fatalf("legacy Extract must keep read_file")
	}
}

func TestExtractAllowed_ObjectArguments(t *testing.T) {
	// Модель иногда отдаёт arguments объектом, а не строкой.
	text := "{\"tool_calls\":[{\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":\"Bash\",\"arguments\":{\"command\":\"ls\"}}}]}"
	calls, _, ok := DefaultBridge.ExtractAllowed(text, []string{"Bash"})
	if !ok || len(calls) != 1 {
		t.Fatalf("object args failed: %+v", calls)
	}
	if !strings.Contains(calls[0].Arguments, "ls") {
		t.Fatalf("args not normalized: %q", calls[0].Arguments)
	}
}

func TestExtractAllowed_MissingID(t *testing.T) {
	text := "```json\n{\"tool_calls\":[{\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{}\"}}]}\n```"
	calls, clean, ok := DefaultBridge.ExtractAllowed(text, []string{"Read"})
	if !ok || len(calls) != 1 {
		t.Fatalf("missing id failed")
	}
	if calls[0].ID == "" {
		t.Fatalf("id must be generated")
	}
	if strings.Contains(clean, "tool_calls") {
		t.Fatalf("clean must cut block: %q", clean)
	}
}

func TestPromptInjectionStatesToolsAreReal(t *testing.T) {
	p := DefaultBridge.PromptInjection([]Tool{{Name: "Edit", Description: "edit"}}, "auto")
	if !strings.Contains(p, "not role-play") || !strings.Contains(p, "harness executes") {
		t.Fatalf("injection must address the pretend-output refusal: %q", p)
	}
}

func TestExtractAllowed_UnwrapsDoubleEncodedArgs(t *testing.T) {
	// Живой кейс gpt-5.4-nano 2026-09-12: arguments — JSON-строка в строке.
	inner, _ := json.Marshal(map[string]string{"path": "a.txt"})
	once, _ := json.Marshal(string(inner)) // двойное кодирование моделью
	wrapper, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{
		"id": "call_1", "type": "function",
		"function": map[string]any{"name": "Edit", "arguments": json.RawMessage(once)},
	}}})
	calls, _, ok := DefaultBridge.ExtractAllowed(string(wrapper), []string{"Edit"})
	if !ok || len(calls) != 1 {
		t.Fatalf("extract failed: %+v", calls)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(calls[0].Arguments), &v); err != nil {
		t.Fatalf("args not unwrapped to object: %q", calls[0].Arguments)
	}
	if v["path"] != "a.txt" {
		t.Fatalf("args = %v", v)
	}
}

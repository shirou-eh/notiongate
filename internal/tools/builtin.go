package tools

import "encoding/json"

var DefaultRegistry = NewRegistry()
var DefaultBridge = NewBridge(DefaultRegistry)

func init() {
	// Встроенные тулзы — всегда могут активироваться сами.
	// Плагины могут добавлять свои через Registry.Register.
	must := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	DefaultRegistry.Register(Tool{
		Name:        "read_file",
		Description: "Read file from workspace",
		Parameters:  must(map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}),
	})
	DefaultRegistry.Register(Tool{
		Name:        "write_file",
		Description: "Write file to workspace",
		Parameters:  must(map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}),
	})
	DefaultRegistry.Register(Tool{
		Name:        "exec",
		Description: "Execute shell command",
		Parameters:  must(map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}}),
	})
	DefaultRegistry.Register(Tool{
		Name:        "web_search",
		Description: "Search web",
		Parameters:  must(map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}),
	})
}

// Package example is a minimal Go plugin showing how to add full
// customization to notiongate.
//
// Copy this folder to plugins/<yourname>/, rename the package, implement
// your logic — one blank import in cmd/notiongate/main.go (see plugins/README.md)
// and your plugin appears in `notiongate plugins list` and `POST /admin/plugins/*`.
//
// No core files are touched.
package example

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shirou-eh/notiongate/internal/plugin"
)

func init() {
	plugin.Register(&Plugin{})
}

// Plugin demonstrates every optional capability.
type Plugin struct{}

func (p *Plugin) Name() string        { return "example" }
func (p *Plugin) Description() string { return "Пример плагина — шаблон для копирования (Go, полная кастомизация)" }
func (p *Plugin) Version() string     { return "0.1.0" }

func (p *Plugin) Init(_ context.Context, deps plugin.Dependencies) error {
	// deps.Config, deps.Store, deps.Pool are available here.
	// Example: validate that DB is reachable, start background goroutine, etc.
	_ = deps
	return nil
}

func (p *Plugin) Close() error { return nil }

// --- Autoreg capability (optional) ---

func (p *Plugin) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"note":"example provider has no real autoreg — returns mock tokens","prefix":"example"}`)
}

func (p *Plugin) ValidateConfig(cfg json.RawMessage) error {
	if len(cfg) == 0 {
		return nil
	}
	var tmp map[string]any
	if err := json.Unmarshal(cfg, &tmp); err != nil {
		return fmt.Errorf("example: bad JSON: %w", err)
	}
	return nil
}

func (p *Plugin) CreateAccounts(_ context.Context, cfg json.RawMessage, opts plugin.CreateOptions) ([]plugin.CreatedAccount, error) {
	// Real autoreg would: create mailboxes, solve captchas, call Notion API,
	// extract token_v2 here. This mock just returns deterministic fake tokens
	// so the integration can be tested without external dependencies.
	_ = cfg
	if opts.Count <= 0 {
		opts.Count = 1
	}
	if opts.Count > 20 {
		return nil, fmt.Errorf("example: count too large (max 20)")
	}
	prefix := opts.LabelPrefix
	if prefix == "" {
		prefix = "example"
	}
	out := make([]plugin.CreatedAccount, 0, opts.Count)
	for i := 0; i < opts.Count; i++ {
		out = append(out, plugin.CreatedAccount{
			TokenV2: fmt.Sprintf("v02:mock-%s-%d", prefix, i+1),
			Label:   fmt.Sprintf("%s-%d", prefix, i+1),
			Proxy:   opts.Proxy,
		})
	}
	return out, nil
}

// --- Hook capability (optional) ---

// OnAccountAdded is called after any account enters the pool.
func (p *Plugin) OnAccountAdded(acc interface{}) {}

// Ensure compile-time interface compliance.
var _ plugin.AutoregProvider = (*Plugin)(nil)

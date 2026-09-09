// Package autoregexample is a realistic autoreg template. Drop it as
// plugins/autoreg-example/ and replace the TODO sections with your actual
// registration flow (mail provider, captcha solver, Notion onboarding).
//
// It lives in its own folder with its own config.json — zero coupling to core.
// Register with one blank import in cmd/notiongate/main.go.

package autoregexample

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shirou-eh/notiongate/internal/plugin"
)

func init() {
	plugin.Register(&Plugin{})
}

type Plugin struct {
	cfg Config
}

type Config struct {
	MailDomain   string `json:"mail_domain"`
	MailAPIKey   string `json:"mail_api_key"`
	CaptchaKey   string `json:"captcha_key"`
	ProxyURL     string `json:"proxy_url"`
	UseCapsolver bool   `json:"use_capsolver"`
}

func (p *Plugin) Name() string        { return "autoreg-example" }
func (p *Plugin) Description() string { return "Шаблон авторега Notion — замени TODO на свой флоу (почта → капча → Notion → token_v2)" }
func (p *Plugin) Version() string     { return "0.1.0" }

func (p *Plugin) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{
  "mail_domain": "example.com",
  "mail_api_key": "changeme",
  "captcha_key": "",
  "proxy_url": "",
  "use_capsolver": false
}`)
}

func (p *Plugin) ValidateConfig(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("autoreg-example: config.json is required")
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.MailDomain == "" {
		return fmt.Errorf("mail_domain is required")
	}
	return nil
}

func (p *Plugin) Init(ctx context.Context, deps plugin.Dependencies) error {
	// Load isolated config for this plugin
	raw := plugin.ProviderConfig(deps.Config.PluginsDir, p.Name())
	if len(raw) == 0 {
		raw = p.DefaultConfig()
	}
	var c Config
	_ = json.Unmarshal(raw, &c)
	p.cfg = c
	_ = ctx
	_ = deps
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) CreateAccounts(ctx context.Context, cfg json.RawMessage, opts plugin.CreateOptions) ([]plugin.CreatedAccount, error) {
	// Resolve effective config (per-call overrides file config)
	eff := p.cfg
	if len(cfg) > 0 && string(cfg) != "null" && string(cfg) != "{}" {
		_ = json.Unmarshal(cfg, &eff)
	}
	if err := p.ValidateConfig(mustJSON(eff)); err != nil {
		return nil, err
	}
	if opts.Count <= 0 {
		opts.Count = 1
	}
	// ---- TODO: your real autoreg flow here ----
	// 1. Create mailbox via eff.MailAPIKey / eff.MailDomain
	// 2. Solve captcha via eff.CaptchaKey
	// 3. POST to Notion onboarding, follow redirects
	// 4. Extract token_v2 from Set-Cookie
	// For template we simulate with delay:
	select {
	case <-time.After(time.Duration(opts.Count) * 300 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([]plugin.CreatedAccount, 0, opts.Count)
	for i := 0; i < opts.Count; i++ {
		out = append(out, plugin.CreatedAccount{
			TokenV2: fmt.Sprintf("v02:autoreg-%s-%d-needs-real-implementation", eff.MailDomain, i+1),
			Label:   fmt.Sprintf("autoreg-%d", i+1),
			Proxy:   opts.Proxy,
		})
	}
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var _ plugin.AutoregProvider = (*Plugin)(nil)

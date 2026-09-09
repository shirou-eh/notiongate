package plugin

import (
	"context"
	"encoding/json"
	"fmt"
)

// CreateOptions alias for convenience — same shape as provider.CreateOptions
// but exposed at package level for callers that only import plugin.
func CreateWithProvider(ctx context.Context, providerName string, cfg json.RawMessage, opts CreateOptions) ([]CreatedAccount, error) {
	p, ok := Get(providerName)
	if !ok {
		return nil, fmt.Errorf("plugin %q not found (available: %v)", providerName, listNames())
	}
	ap, ok := p.(AutoregProvider)
	if !ok {
		return nil, fmt.Errorf("plugin %q does not support autoreg", providerName)
	}
	if err := ap.ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("plugin %q config invalid: %w", providerName, err)
	}
	return ap.CreateAccounts(ctx, cfg, opts)
}

func listNames() []string {
	out := []string{}
	for _, p := range List() {
		out = append(out, p.Name())
	}
	return out
}

package plugin

import (
	"fmt"
	"regexp"
	"sync"
)

var validNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("plugin: empty name")
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("plugin: invalid name %q (must match %s)", name, validNameRe.String())
	}
	return nil
}

// ValidateName is exported for admin/API callers.
func ValidateName(name string) error { return validateName(name) }

// ValidateProxyURL checks proxy URL without leaking credentials in errors.
func ValidateProxyURL(proxyURL string) error {
	if proxyURL == "" {
		return nil
	}
	if len(proxyURL) > 2048 {
		return fmt.Errorf("proxy URL too long")
	}
	// Basic scheme check without parsing credentials
	for _, c := range proxyURL {
		if c == '\n' || c == '\r' || c == '\x00' {
			return fmt.Errorf("proxy URL contains invalid characters")
		}
	}
	// Reuse notion's strict validator via helper to avoid import cycle — duplicate logic
	if len(proxyURL) < 5 {
		return fmt.Errorf("proxy URL too short")
	}
	// Must have scheme://
	if !contains(proxyURL, "://") {
		return fmt.Errorf("proxy URL must have scheme (http/https/socks5)")
	}
	return nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

var (
	mu       sync.RWMutex
	registry = map[string]Plugin{}
)

// Register adds a plugin to the global registry.
// Panics on duplicate name — duplicate plugin folders are a hard error.
func Register(p Plugin) {
	if p == nil {
		panic("plugin: Register(nil)")
	}
	if err := validateName(p.Name()); err != nil {
		panic(err.Error())
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[p.Name()]; exists {
		panic(fmt.Sprintf("plugin: duplicate registration %q", p.Name()))
	}
	registry[p.Name()] = p
}

// MustRegister is like Register but returns error instead of panicking — for
// dynamic (exec) plugins discovered at runtime.
func MustRegister(p Plugin) error {
	if p == nil {
		return fmt.Errorf("plugin: nil")
	}
	if err := validateName(p.Name()); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[p.Name()]; exists {
		return fmt.Errorf("plugin: duplicate %q", p.Name())
	}
	registry[p.Name()] = p
	return nil
}

// Get returns a plugin by name.
func Get(name string) (Plugin, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// List returns all registered plugins.
func List() []Plugin {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Plugin, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	return out
}

// ListAutoreg returns only providers with Autoreg capability.
func ListAutoreg() []AutoregProvider {
	mu.RLock()
	defer mu.RUnlock()
	var out []AutoregProvider
	for _, p := range registry {
		if ap, ok := p.(AutoregProvider); ok {
			out = append(out, ap)
		}
	}
	return out
}

// Reset clears the registry — used only in tests.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]Plugin{}
}

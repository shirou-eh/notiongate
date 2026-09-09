// Package plugin defines the notiongate plugin system — full customization
// without touching core. Every external capability (autoreg, hooks, middleware)
// is a Plugin living in its own folder with its own config.
package plugin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/store"
)

type Dependencies struct {
	Config *config.Config
	Store  *store.Store
	Pool   *pool.Pool
}

// Plugin is the base interface every plugin implements.
// Name must be unique (used as directory name & registry key).
type Plugin interface {
	Name() string
	Description() string
	Version() string
	Init(ctx context.Context, deps Dependencies) error
	Close() error
}

// AutoregProvider — optional capability. If a plugin implements this,
// it can create Notion accounts (or any accounts that yield token_v2).
// The core calls it via Manager.CreateAccounts.
type AutoregProvider interface {
	Plugin
	ValidateConfig(cfg json.RawMessage) error
	DefaultConfig() json.RawMessage
	CreateAccounts(ctx context.Context, cfg json.RawMessage, opts CreateOptions) ([]CreatedAccount, error)
}

type CreateOptions struct {
	Count       int    `json:"count"`
	Proxy       string `json:"proxy,omitempty"`
	LabelPrefix string `json:"label_prefix,omitempty"`
}

// CreatedAccount is what an autoreg plugin returns. Only TokenV2 is
// required — the core will bootstrap space/user discovery. Extra fields
// allow the provider to pre-fill metadata without an extra round-trip.
type CreatedAccount struct {
	TokenV2   string `json:"token_v2"`
	Label     string `json:"label,omitempty"`
	Email     string `json:"email,omitempty"`
	Proxy     string `json:"proxy,omitempty"`
	SpaceID   string `json:"space_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
}

// HTTPProvider — optional capability for plugins that need routes or
// middleware. Full customization: inject any handler.
type HTTPProvider interface {
	Plugin
	// RegisterRoutes is called once at startup. prefix is "/plugins/<name>".
	// Use auth wrapper for protected routes if needed.
	RegisterRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc)
	// Middleware wraps every request (optional — return next unchanged if not needed).
	Middleware(next http.Handler) http.Handler
}

// HookProvider — optional lifecycle hooks.
type HookProvider interface {
	Plugin
	OnAccountAdded(acc model.Account)
	OnAccountRemoved(id string)
}

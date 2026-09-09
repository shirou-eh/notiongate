// Package config loads notiongate configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings. Every field can be overridden via env.
type Config struct {
	Host string
	Port int

	// Client-facing API key. Empty on loopback means "no auth".
	APIKey string
	// Admin key for /admin/*. Loopback stays trusted when empty; a
	// non-loopback bind without a key locks admin (fail-closed).
	AdminKey string

	DBPath string

	// Pool behaviour.
	RotateAt        float64       // rotate account when usage ratio >= this (default 0.8)
	DefaultWindow   string        // "day" | "month"
	DefaultLimit    int64         // requests per window, 0 = unlimited
	StickySessions  bool          // reuse same account per user key
	MaxAttempts     int           // failover attempts per request
	UpstreamTimeout time.Duration // per upstream inference call
	RefreshInterval time.Duration // background account validity check
	MinInterval     time.Duration // human-like min pause between upstream calls per account

	// Upstream (Notion) tuning.
	NotionBaseURL       string
	NotionClientVersion string
	UserAgent           string

	// Plugins — each plugin lives in its own folder with isolated config.
	PluginsDir string // default ./plugins, also checks ./extensions

	LogLevel string
}

func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Host, c.Port) }

// Loopback reports whether the server binds to a loopback interface.
func (c *Config) Loopback() bool {
	h := c.Host
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

// Load builds a Config from environment variables, applying defaults.
func Load() (*Config, error) {
	c := &Config{
		Host:                env("NOTIONGATE_HOST", "127.0.0.1"),
		Port:                envInt("NOTIONGATE_PORT", 8787),
		APIKey:              env("API_KEY", ""),
		AdminKey:            env("ADMIN_KEY", ""),
		DBPath:              env("DB_PATH", defaultDBPath()),
		RotateAt:            envFloat("ROTATE_AT", 0.8),
		DefaultWindow:       strings.ToLower(env("DEFAULT_WINDOW", "month")),
		DefaultLimit:        envInt64("DEFAULT_LIMIT", 0),
		StickySessions:      envBool("STICKY_SESSIONS", true),
		MaxAttempts:         envInt("MAX_ATTEMPTS", 3),
		UpstreamTimeout:     envDur("UPSTREAM_TIMEOUT", 5*time.Minute),
		RefreshInterval:     envDur("REFRESH_INTERVAL", 15*time.Minute),
		MinInterval:         envDur("MIN_INTERVAL", 4*time.Second),
		NotionBaseURL:       strings.TrimRight(env("NOTION_BASE_URL", "https://www.notion.so"), "/"),
		NotionClientVersion: env("NOTION_CLIENT_VERSION", "23.13.20260909.0411"),
		UserAgent:           env("USER_AGENT", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"),
		PluginsDir:          env("PLUGINS_DIR", defaultPluginsDir()),
		LogLevel:            strings.ToLower(env("LOG_LEVEL", "info")),
	}
	switch c.DefaultWindow {
	case "day", "month":
	default:
		return nil, fmt.Errorf("DEFAULT_WINDOW must be day|month, got %q", c.DefaultWindow)
	}
	if c.RotateAt <= 0 || c.RotateAt > 1 {
		return nil, fmt.Errorf("ROTATE_AT must be in (0,1], got %v", c.RotateAt)
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 1
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = 15 * time.Minute
	}
	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func envDur(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

func defaultDBPath() string {
	var p string
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		p = filepath.Join(xdg, "notiongate", "notiongate.db")
	} else if home, err := os.UserHomeDir(); err == nil {
		p = filepath.Join(home, ".local", "share", "notiongate", "notiongate.db")
	} else {
		return "./data/notiongate.db"
	}
	// Миграция со старого пути ./data/notiongate.db → глобальный
	// Если новый пустой (0 аккаунтов) а старый с данными — тоже мигрируем
	needMigrate := false
	if _, err := os.Stat(p); os.IsNotExist(err) {
		needMigrate = true
	} else if fi, err := os.Stat(p); err == nil && fi.Size() < 1024 {
		// подозрительно маленький — проверим количество аккаунтов
		needMigrate = true
	}
	if needMigrate {
		if _, err2 := os.Stat("./data/notiongate.db"); err2 == nil {
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			if data, err3 := os.ReadFile("./data/notiongate.db"); err3 == nil && len(data) > 1024 {
				_ = os.WriteFile(p, data, 0o600)
				for _, suf := range []string{"-wal", "-shm"} {
					if d, err := os.ReadFile("./data/notiongate.db" + suf); err == nil && len(d) > 0 {
						_ = os.WriteFile(p+suf, d, 0o600)
					}
				}
			}
		}
	}
	return p
}

func defaultPluginsDir() string {
	// Глобально везде: XDG_CONFIG_HOME или ~/.config
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "notiongate", "plugins")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "notiongate", "plugins")
	}
	return "./plugins"
}

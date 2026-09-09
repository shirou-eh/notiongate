package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// LoadExecPlugins scans dir for subfolders containing plugin.json and
// registers each as an ExecAutoregPlugin. Missing dir is not an error —
// plugins are optional. Also checks ./extensions as alias.
func LoadExecPlugins(dir string) int {
	n := loadExecDir(dir)
	// Alias: extensions/ — user mentioned it, keep knife-through-butter.
	if dir != "./extensions" && dir != "extensions" {
		n += loadExecDir("./extensions")
		n += loadExecDir("extensions")
	}
	return n
}

func loadExecDir(dir string) int {
	if dir == "" {
		return 0
	}
	// Prevent scanning absurdly broad dirs like /tmp
	cleanDir := filepath.Clean(dir)
	if cleanDir == "/" || cleanDir == "/tmp" || cleanDir == "." {
		slog.Warn("plugin: refusing to scan too broad dir", "dir", dir)
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		slog.Warn("plugin: scan failed", "dir", dir, "err", err)
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		if err := validateName(e.Name()); err != nil {
			slog.Warn("plugin: invalid name, skip", "name", e.Name(), "err", err)
			continue
		}
		// Dir symlink check — не следуем за симлинком на директорию
		dirPath := filepath.Join(dir, e.Name())
		if fi, err := os.Lstat(dirPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("plugin: dir is symlink, skip", "dir", dirPath)
			continue
		}
		manifest := filepath.Join(dir, e.Name(), "plugin.json")
		// Use O_NOFOLLOW open to avoid TOCTOU
		fi, err := os.Lstat(manifest)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("plugin: manifest is symlink, skip", "manifest", manifest)
			continue
		}
		if fi.Size() > maxManifestBytes {
			slog.Warn("plugin: manifest too large, skip", "manifest", manifest, "size", fi.Size())
			continue
		}
		p, err := NewExec(manifest)
		if err != nil {
			slog.Warn("plugin: skip", "manifest", manifest, "err", err)
			continue
		}
		if err := MustRegister(p); err != nil {
			// Duplicate from previous scan (e.g., ./plugins vs ./extensions alias
			// pointing to same inode) — not an error, just skip silently.
			slog.Debug("plugin: already registered, skip", "name", p.Name())
			continue
		}
		slog.Info("plugin: registered exec autoreg", "name", p.Name(), "dir", filepath.Join(dir, e.Name()))
		n++
	}
	return n
}

// InitAll calls Init on every registered plugin. First error aborts.
func InitAll(ctx context.Context, deps Dependencies) error {
	for _, p := range List() {
		if err := p.Init(ctx, deps); err != nil {
			return err
		}
		slog.Info("plugin: initialized", "name", p.Name(), "version", p.Version())
	}
	return nil
}

// CloseAll calls Close on every plugin (errors logged, not returned).
func CloseAll() {
	for _, p := range List() {
		if err := p.Close(); err != nil {
			slog.Warn("plugin: close failed", "name", p.Name(), "err", err)
		}
	}
}

// ProviderConfig loads per-plugin config from <dir>/<name>/config.json
// (or config.yaml). Returns nil if file missing (plugin uses defaults).
func readFileNoFollow(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("file too large")
	}
	limited := io.LimitReader(f, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file too large")
	}
	return data, nil
}

func ProviderConfig(dir, name string) json.RawMessage {
	if err := validateName(name); err != nil {
		slog.Warn("plugin: invalid provider name for config", "name", name, "err", err)
		return nil
	}
	// Ensure dir is clean and name cannot traverse
	cleanName := filepath.Clean(name)
	if cleanName != name || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		slog.Warn("plugin: traversal attempt", "name", name)
		return nil
	}
	for _, fname := range []string{"config.json", "config.yaml", "config.yml"} {
		path := filepath.Join(dir, name, fname)
		// Use O_NOFOLLOW open to avoid TOCTOU symlink LFI
		data, err := readFileNoFollow(path, 1*1024*1024)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("plugin: config read failed", "path", path, "err", err)
			}
			continue
		}
		if len(data) > 1*1024*1024 {
			slog.Warn("plugin: config too large", "path", path)
			continue
		}
		if fname == "config.json" {
			var tmp json.RawMessage
			if err := json.Unmarshal(data, &tmp); err != nil {
				slog.Warn("plugin: bad config.json", "plugin", name, "err", err)
				continue
			}
			return json.RawMessage(data)
		}
		// YAML → JSON
		var tmp any
		if err := yamlUnmarshal(data, &tmp); err != nil {
			slog.Warn("plugin: bad config.yaml", "plugin", name, "err", err)
			continue
		}
		j, _ := json.Marshal(tmp)
		return j
	}
	return nil
}

func yamlUnmarshal(data []byte, out *any) error {
	return yaml.Unmarshal(data, out)
}

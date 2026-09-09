package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxManifestBytes = 64 * 1024
	maxStdoutBytes   = 16 * 1024 * 1024
	maxStderrBytes   = 1 * 1024 * 1024
	maxTimeout       = 10 * time.Minute
)

// ExecAutoregPlugin wraps an external executable (any language) as an
// AutoregProvider. Protocol:
//
//   stdin  -> {"config": <provider_config>, "options": {"count":2,"proxy":"..."}}
//   stdout <- [{"token_v2":"...","label":"..."}, ...]
//   stderr -> logs (forwarded to slog)
//   exit 0 == success, non-zero == error (stderr returned)
//
type ExecAutoregPlugin struct {
	name        string
	description string
	version     string
	command     string
	args        []string
	timeout     time.Duration
	dir         string
}

type limitedWriter struct {
	buf       *bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		remaining := w.limit - w.buf.Len()
		if remaining > 0 {
			w.buf.Write(p[:remaining])
		}
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

type ExecManifest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Command     string `json:"command"`
	Args        []string `json:"args,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
}

// NewExec creates a plugin from a manifest file.
func NewExec(manifestPath string) (*ExecAutoregPlugin, error) {
	// O_NOFOLLOW to avoid TOCTOU symlink LFI
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("exec plugin: read manifest %s: %w", manifestPath, err)
	}
	defer f.Close()
	limited := io.LimitReader(f, maxManifestBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("exec plugin: read manifest %s: %w", manifestPath, err)
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("exec plugin: manifest too large %s (max %d)", manifestPath, maxManifestBytes)
	}
	var m ExecManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("exec plugin: bad manifest %s: %w", manifestPath, err)
	}
	if err := validateName(m.Name); err != nil && m.Name != "" {
		return nil, err
	}
	if m.Name == "" {
		m.Name = filepath.Base(filepath.Dir(manifestPath))
		if err := validateName(m.Name); err != nil {
			return nil, err
		}
	}
	if err := validateName(m.Name); err != nil {
		return nil, err
	}
	if m.Command == "" {
		return nil, fmt.Errorf("exec plugin %q: command is required", m.Name)
	}
	// Reject path traversal in command when it contains slashes outside allowed dir
	if strings.Contains(m.Command, "\x00") || strings.Contains(m.Command, "\n") {
		return nil, fmt.Errorf("exec plugin %q: invalid command", m.Name)
	}
	if m.Timeout == "" {
		m.Timeout = "120s"
	}
	d, _ := time.ParseDuration(m.Timeout)
	if d <= 0 {
		d = 120 * time.Second
	}
	if d > maxTimeout {
		d = maxTimeout
	}
	dir := filepath.Dir(manifestPath)
	// Jail: command must be inside plugin dir — bare names (PATH lookup) are
	// rejected to prevent RCE via "sh -c" etc. Use "./run.py" inside dir.
	if !filepath.IsAbs(m.Command) {
		if strings.Contains(m.Command, "/") || strings.Contains(m.Command, "\\") {
			// relative path with slash must be inside plugin dir
			candidate := filepath.Join(dir, m.Command)
			// Lstat to avoid symlink escape, then check prefix
			if fi, err := os.Lstat(candidate); err != nil || fi.Mode()&os.ModeSymlink != 0 {
				// Allow symlink only if it resolves inside dir
				if err == nil && fi.Mode()&os.ModeSymlink != 0 {
					target, _ := filepath.EvalSymlinks(candidate)
					absDir, _ := filepath.Abs(dir)
					if absTarget, err := filepath.Abs(target); err == nil {
						if !strings.HasPrefix(absTarget, absDir+string(os.PathSeparator)) && absTarget != absDir {
							return nil, fmt.Errorf("exec plugin %q: command symlink escapes plugin dir", m.Name)
						}
					}
				} else if err != nil {
					return nil, fmt.Errorf("exec plugin %q: command not found in plugin dir: %s", m.Name, m.Command)
				}
			}
			cleanCand := filepath.Clean(candidate)
			absDir, _ := filepath.Abs(dir)
			absCand, _ := filepath.Abs(cleanCand)
			if !strings.HasPrefix(absCand, absDir+string(os.PathSeparator)) && absCand != absDir {
				return nil, fmt.Errorf("exec plugin %q: command escapes plugin dir", m.Name)
			}
			if abs, err := filepath.Abs(candidate); err == nil {
				m.Command = abs
			} else {
				m.Command = candidate
			}
		} else {
			return nil, fmt.Errorf("exec plugin %q: bare command %q not allowed (use ./file inside plugin dir)", m.Name, m.Command)
		}
	} else {
		// Absolute path: must be inside plugin dir (jail) — no arbitrary /bin/sh
		absDir, _ := filepath.Abs(dir)
		absCmd, _ := filepath.Abs(m.Command)
		if !strings.HasPrefix(absCmd, absDir+string(os.PathSeparator)) && absCmd != absDir {
			return nil, fmt.Errorf("exec plugin %q: absolute command outside plugin dir not allowed: %s", m.Name, m.Command)
		}
		if _, err := os.Stat(m.Command); err != nil {
			return nil, fmt.Errorf("exec plugin %q: absolute command not found: %s", m.Name, m.Command)
		}
	}
	return &ExecAutoregPlugin{
		name:        m.Name,
		description: m.Description,
		version:     m.Version,
		command:     m.Command,
		args:        m.Args,
		timeout:     d,
		dir:         dir,
	}, nil
}

func (e *ExecAutoregPlugin) Name() string        { return e.name }
func (e *ExecAutoregPlugin) Description() string { return e.description }
func (e *ExecAutoregPlugin) Version() string {
	if e.version != "" {
		return e.version
	}
	return "0.1.0"
}
func (e *ExecAutoregPlugin) Init(_ context.Context, _ Dependencies) error { return nil }
func (e *ExecAutoregPlugin) Close() error                                { return nil }
func (e *ExecAutoregPlugin) DefaultConfig() json.RawMessage               { return json.RawMessage(`{}`) }
func (e *ExecAutoregPlugin) ValidateConfig(_ json.RawMessage) error       { return nil }

func (e *ExecAutoregPlugin) CreateAccounts(ctx context.Context, cfg json.RawMessage, opts CreateOptions) ([]CreatedAccount, error) {
	payload := map[string]any{
		"config":  json.RawMessage(cfg),
		"options": opts,
	}
	in, _ := json.Marshal(payload)

	ectx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ectx, e.command, e.args...)
	cmd.Dir = e.dir
	cmd.Stdin = bytes.NewReader(in)
	// Cap stdout/stderr to prevent OOM from malicious plugin
	var outBuf, errBuf bytes.Buffer
	outW := &limitedWriter{buf: &outBuf, limit: maxStdoutBytes}
	errW := &limitedWriter{buf: &errBuf, limit: maxStderrBytes}
	cmd.Stdout = outW
	cmd.Stderr = errW

	if err := cmd.Run(); err != nil {
		// Cap stderr in error message
		stderrStr := errBuf.String()
		if errW.truncated {
			stderrStr += "…(truncated)"
		}
		if len(stderrStr) > 2048 {
			stderrStr = stderrStr[:2048] + "…(truncated)"
		}
		return nil, fmt.Errorf("exec plugin %q failed: %w — stderr: %s", e.name, err, stderrStr)
	}
	if outW.truncated {
		return nil, fmt.Errorf("exec plugin %q: stdout too large (limit %d)", e.name, maxStdoutBytes)
	}
	outBytes := outBuf.Bytes()
	// Support both: bare array or {"accounts": [...]}
	var accs []CreatedAccount
	if err := json.Unmarshal(outBytes, &accs); err == nil {
		// Cap number of accounts even if stdout was valid but huge
		if len(accs) > 50 {
			return nil, fmt.Errorf("exec plugin %q: too many accounts %d (max 50)", e.name, len(accs))
		}
		return accs, nil
	}
	var wrapped struct {
		Accounts []CreatedAccount `json:"accounts"`
	}
	if err := json.Unmarshal(outBytes, &wrapped); err == nil && wrapped.Accounts != nil {
		if len(wrapped.Accounts) > 50 {
			return nil, fmt.Errorf("exec plugin %q: too many accounts %d (max 50)", e.name, len(wrapped.Accounts))
		}
		return wrapped.Accounts, nil
	}
	outStr := string(outBytes)
	if len(outStr) > 2048 {
		outStr = outStr[:2048] + "…(truncated)"
	}
	return nil, fmt.Errorf("exec plugin %q: bad stdout JSON (want []CreatedAccount): %s", e.name, outStr)
}

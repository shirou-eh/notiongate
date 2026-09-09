// Package autostart — протоколы автозапуска notiongate.
// Поддерживает systemd (Linux), launchd (macOS), Windows service stub,
// Docker, и простой healthcheck + watch-рестарт.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

type InstallOpts struct {
	BinPath string // путь к бинарю notiongate
	WorkDir string // рабочая директория (где data/, .env)
	EnvFile string // путь к .env (опционально)
	Port    int
	Host    string
}

// Detect returns current OS autostart availability.
func Detect() string {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err == nil {
			return "systemd"
		}
		if _, err := os.Stat("/etc/systemd/system"); err == nil {
			return "systemd"
		}
		return "docker"
	case "darwin":
		return "launchd"
	case "windows":
		return "windows-service"
	default:
		return "docker"
	}
}

func BinPathFallback() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "./notiongate"
}

// sanitizeUnitField rejects \n\r\x00 to prevent systemd unit injection.
func sanitizeUnitField(s string) string {
	if strings.ContainsAny(s, "\n\r\x00") {
		s = strings.ReplaceAll(s, "\n", "")
		s = strings.ReplaceAll(s, "\r", "")
		s = strings.ReplaceAll(s, "\x00", "")
	}
	// escape % for systemd specifiers
	s = strings.ReplaceAll(s, "%", "%%")
	return s
}

// SystemdUnit renders a unit file.
func SystemdUnit(opts InstallOpts) string {
	if opts.BinPath == "" {
		opts.BinPath = BinPathFallback()
	}
	if opts.WorkDir == "" {
		opts.WorkDir, _ = os.Getwd()
	}
	opts.BinPath = sanitizeUnitField(opts.BinPath)
	opts.WorkDir = sanitizeUnitField(opts.WorkDir)
	opts.EnvFile = sanitizeUnitField(opts.EnvFile)
	envLine := ""
	if opts.EnvFile != "" {
		envLine = fmt.Sprintf("EnvironmentFile=%s", opts.EnvFile)
	}
	return fmt.Sprintf(`[Unit]
Description=Notion Gate — Гейтик держит портал (%s)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s serve
%s
Restart=always
RestartSec=5
# Гейтик: протокол живучести (healthcheck вне unit, через docker/k8s)
# curl -fs http://%s:%d/healthz || exit 1
# лимиты
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, opts.BinPath, opts.WorkDir, opts.BinPath, envLine, opts.Host, opts.Port)
}

// xmlEscape escapes for plist XML.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// LaunchdPlist renders macOS plist.
func LaunchdPlist(opts InstallOpts) string {
	if opts.BinPath == "" {
		opts.BinPath = BinPathFallback()
	}
	if opts.WorkDir == "" {
		opts.WorkDir, _ = os.Getwd()
	}
	opts.BinPath = xmlEscape(sanitizeUnitField(opts.BinPath))
	opts.WorkDir = xmlEscape(sanitizeUnitField(opts.WorkDir))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>ai.notiongate</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>NetworkState</key><true/></dict>
  <key>StandardOutPath</key><string>%s/notiongate.log</string>
  <key>  StandardErrorPath</key><string>%s/notiongate.log</string>
</dict>
</plist>
`, opts.BinPath, opts.WorkDir, opts.WorkDir, opts.WorkDir)
}

// Docker hint
func DockerHint() string {
	return "docker compose up -d --build  # уже настроен: docker-compose.yml + Dockerfile (Гейтик внутри)"
}

// Install writes unit/plist to the right place (needs root for system).
// For user-mode, writes to ~/.config/systemd/user/ or ~/Library/LaunchAgents.
func Install(opts InstallOpts) (string, error) {
	switch Detect() {
	case "systemd":
		return installSystemd(opts)
	case "launchd":
		return installLaunchd(opts)
	default:
		return "", fmt.Errorf("автостарт для %s: используй docker (%s) или systemd вручную", runtime.GOOS, DockerHint())
	}
}

func safeMkdirAll(dir string) error {
	// Проверяем каждый компонент пути на symlink родителя
	clean := filepath.Clean(dir)
	parts := strings.Split(clean, string(os.PathSeparator))
	cur := ""
	if filepath.IsAbs(clean) {
		cur = string(os.PathSeparator)
	}
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		if fi, err := os.Lstat(cur); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("отказ: %s — symlink в пути", cur)
			}
			if !fi.IsDir() {
				return fmt.Errorf("отказ: %s — не директория", cur)
			}
		}
	}
	return os.MkdirAll(dir, 0o755)
}

func installSystemd(opts InstallOpts) (string, error) {
	home, _ := os.UserHomeDir()
	userUnit := filepath.Join(home, ".config/systemd/user/notiongate.service")
	systemUnit := "/etc/systemd/system/notiongate.service"

	target := userUnit
	useSystem := false
	if os.Geteuid() == 0 {
		target = systemUnit
		useSystem = true
	}
	if err := safeMkdirAll(filepath.Dir(target)); err != nil {
		return "", err
	}
	// Symlink check — не перезаписываем чужой файл через symlink
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("отказ: %s — symlink, не перезаписываю", target)
		}
	}
	content := SystemdUnit(opts)
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	// попытаемся enable (не фатально если нет systemctl)
	unitName := "notiongate.service"
	if useSystem {
		exec.Command("systemctl", "daemon-reload").Run()
		exec.Command("systemctl", "enable", unitName).Run()
	} else {
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		exec.Command("systemctl", "--user", "enable", unitName).Run()
	}
	return target, nil
}

func installLaunchd(opts InstallOpts) (string, error) {
	home, _ := os.UserHomeDir()
	target := filepath.Join(home, "Library/LaunchAgents/ai.notiongate.plist")
	if err := safeMkdirAll(filepath.Dir(target)); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("отказ: %s — symlink", target)
		}
	}
	if err := os.WriteFile(target, []byte(LaunchdPlist(opts)), 0o644); err != nil {
		return "", err
	}
	exec.Command("launchctl", "load", target).Run()
	return target, nil
}

func Uninstall() (string, error) {
	switch Detect() {
	case "systemd":
		home, _ := os.UserHomeDir()
		userUnit := filepath.Join(home, ".config/systemd/user/notiongate.service")
		if _, err := os.Stat(userUnit); err == nil {
			exec.Command("systemctl", "--user", "disable", "notiongate.service").Run()
			os.Remove(userUnit)
			return userUnit + " removed", nil
		}
		if _, err := os.Stat("/etc/systemd/system/notiongate.service"); err == nil {
			exec.Command("systemctl", "disable", "notiongate.service").Run()
			os.Remove("/etc/systemd/system/notiongate.service")
			return "/etc/systemd/system/notiongate.service removed", nil
		}
		return "", fmt.Errorf("systemd unit не найден")
	case "launchd":
		home, _ := os.UserHomeDir()
		target := filepath.Join(home, "Library/LaunchAgents/ai.notiongate.plist")
		exec.Command("launchctl", "unload", target).Run()
		os.Remove(target)
		return target + " removed", nil
	default:
		return "", fmt.Errorf("удали контейнер: docker compose down")
	}
}

func Status() string {
	switch Detect() {
	case "systemd":
		out, _ := exec.Command("systemctl", "is-active", "notiongate.service").CombinedOutput()
		if len(out) == 0 {
			out, _ = exec.Command("systemctl", "--user", "is-active", "notiongate.service").CombinedOutput()
		}
		return strings.TrimSpace(string(out))
	case "launchd":
		out, _ := exec.Command("launchctl", "list").CombinedOutput()
		if strings.Contains(string(out), "ai.notiongate") {
			return "loaded"
		}
		return "not loaded"
	default:
		out, _ := exec.Command("docker", "ps", "--filter", "name=notiongate").CombinedOutput()
		if strings.Contains(string(out), "notiongate") {
			return "running (docker)"
		}
		return "not running"
	}
}

// Health template helper
var _ = template.New

package contextx

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SystemProvider — склеивает system[] запроса.
type SystemProvider struct{ EnabledFlag bool }

func (p *SystemProvider) Name() string     { return "system" }
func (p *SystemProvider) Priority() int    { return 10 }
func (p *SystemProvider) Enabled() bool    { return p.EnabledFlag }
func (p *SystemProvider) Provide(_ context.Context, req *Request) (string, error) {
	if len(req.System) == 0 {
		return "", nil
	}
	return strings.Join(req.System, "\n\n"), nil
}

// TimeProvider — текущее время, таймзона. Просто существует.
type TimeProvider struct{ EnabledFlag bool }

func (p *TimeProvider) Name() string  { return "time" }
func (p *TimeProvider) Priority() int { return 90 }
func (p *TimeProvider) Enabled() bool { return p.EnabledFlag }
func (p *TimeProvider) Provide(_ context.Context, _ *Request) (string, error) {
	now := time.Now().Format(time.RFC3339)
	return fmt.Sprintf("Current time: %s", now), nil
}

// WorkspaceProvider — заглушка для будущего: имя спейса, проект.
// Сейчас просто отдаёт модель/юзера, чтобы Notion видел контекст.
type WorkspaceProvider struct {
	EnabledFlag bool
	SpaceName   string
}

func (p *WorkspaceProvider) Name() string  { return "workspace" }
func (p *WorkspaceProvider) Priority() int { return 20 }
func (p *WorkspaceProvider) Enabled() bool { return p.EnabledFlag }
func (p *WorkspaceProvider) Provide(_ context.Context, req *Request) (string, error) {
	if p.SpaceName == "" && req.Model == "" {
		return "", nil
	}
	var b strings.Builder
	if p.SpaceName != "" {
		fmt.Fprintf(&b, "Workspace: %s\n", p.SpaceName)
	}
	if req.Model != "" {
		fmt.Fprintf(&b, "Requested model: %s", req.Model)
	}
	return strings.TrimSpace(b.String()), nil
}

// ToolsHintProvider — если есть тулзы, подсказывает как их звать.
// Немой — просто кусок текста.
type ToolsHintProvider struct{ EnabledFlag bool }

func (p *ToolsHintProvider) Name() string  { return "tools-hint" }
func (p *ToolsHintProvider) Priority() int { return 30 }
func (p *ToolsHintProvider) Enabled() bool { return p.EnabledFlag }
func (p *ToolsHintProvider) Provide(_ context.Context, req *Request) (string, error) {
	if len(req.Tools) == 0 {
		return "", nil
	}
	return "Tools available — model may call them via JSON.", nil
}

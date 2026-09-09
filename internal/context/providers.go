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

// TimeProvider — сам проверяет нужен ли он: только если в последних
// сообщениях есть упоминание времени/даты.
type TimeProvider struct{ EnabledFlag bool }

func (p *TimeProvider) Name() string  { return "time" }
func (p *TimeProvider) Priority() int { return 90 }
func (p *TimeProvider) Enabled() bool { return p.EnabledFlag }
func (p *TimeProvider) Provide(_ context.Context, req *Request) (string, error) {
	if !needsTime(req) {
		return "", nil
	}
	now := time.Now().Format(time.RFC3339)
	return fmt.Sprintf("Current time: %s", now), nil
}

func needsTime(req *Request) bool {
	text := ""
	for i := len(req.Turns) - 1; i >= 0 && i >= len(req.Turns)-3; i-- {
		text += " " + strings.ToLower(req.Turns[i].Text)
	}
	for _, s := range req.System {
		text += " " + strings.ToLower(s)
	}
	triggers := []string{"время", "дата", "сегодня", "завтра", "сейчас", "time", "date", "today", "срок", "дедлайн"}
	for _, t := range triggers {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// WorkspaceProvider — сам проверяет: отдаёт только если в контексте
// упоминаются файлы/проект или явно запрошена модель.
type WorkspaceProvider struct {
	EnabledFlag bool
	SpaceName   string
}

func (p *WorkspaceProvider) Name() string  { return "workspace" }
func (p *WorkspaceProvider) Priority() int { return 20 }
func (p *WorkspaceProvider) Enabled() bool { return p.EnabledFlag }
func (p *WorkspaceProvider) Provide(_ context.Context, req *Request) (string, error) {
	// Сам проверяет нужен ли воркспейс-контекст
	hasFileMention := false
	for _, t := range req.Turns {
		low := strings.ToLower(t.Text)
		if strings.Contains(low, "файл") || strings.Contains(low, "file") || strings.Contains(low, "проект") || strings.Contains(low, "workspace") {
			hasFileMention = true
			break
		}
	}
	if p.SpaceName == "" && req.Model == "" && !hasFileMention {
		return "", nil
	}
	var b strings.Builder
	if p.SpaceName != "" && hasFileMention {
		fmt.Fprintf(&b, "Workspace: %s\n", p.SpaceName)
	}
	if req.Model != "" {
		fmt.Fprintf(&b, "Requested model: %s", req.Model)
	}
	s := strings.TrimSpace(b.String())
	if s == "Requested model: "+req.Model && !hasFileMention {
		// Модель без файлового контекста — не спамим
		if len(req.Turns) < 2 {
			return "", nil
		}
	}
	return s, nil
}

// ToolsHintProvider — сам проверяет: только если реально есть тулзы
// и модель может их вызвать (auto/required).
type ToolsHintProvider struct{ EnabledFlag bool }

func (p *ToolsHintProvider) Name() string  { return "tools-hint" }
func (p *ToolsHintProvider) Priority() int { return 30 }
func (p *ToolsHintProvider) Enabled() bool { return p.EnabledFlag }
func (p *ToolsHintProvider) Provide(_ context.Context, req *Request) (string, error) {
	if len(req.Tools) == 0 {
		return "", nil
	}
	// Проверяет сам: есть ли в последних сообщениях намёк на тулзы
	last := ""
	if len(req.Turns) > 0 {
		last = strings.ToLower(req.Turns[len(req.Turns)-1].Text)
	}
	if strings.Contains(last, "не используй тул") || strings.Contains(last, "без тул") {
		return "", nil
	}
	return "Tools available — model may call them via JSON.", nil
}

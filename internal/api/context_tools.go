package api

import (
	"context"
	"encoding/json"
	"strings"

	contextx "github.com/shirou-eh/notiongate/internal/context"
	"github.com/shirou-eh/notiongate/internal/tools"
	"github.com/shirou-eh/notiongate/internal/translate"
)

// prepareJob enriches ChatJob with context providers and tool auto-activation.
// Это место где "хуйня тулсы" всегда активируется правильно сама.
func prepareJob(ctx context.Context, job *translate.ChatJob) {
	// 1. Контекст — собираем из всех провайдеров (system, time, workspace, tools-hint)
	req := &contextx.Request{
		UserKey: job.UserKey,
		Model:   job.Model,
		System:  job.System,
		Tools:   toToolRefs(job.Tools),
	}
	for _, t := range job.Turns {
		req.Turns = append(req.Turns, contextx.Turn{Role: t.Role, Text: t.Text})
	}
	if ctxStr := contextx.DefaultManager.Build(ctx, req); ctxStr != "" {
		// Вставляем собранный контекст в начало system
		job.System = append([]string{ctxStr}, job.System...)
	}

	// 2. Тулзы — автоактивация
	// Если клиент не прислал тулзы, но есть встроенные и ShouldActivate говорит да — добавляем их
	lastText := ""
	for i := len(job.Turns) - 1; i >= 0; i-- {
		if job.Turns[i].Role == "user" {
			lastText = job.Turns[i].Text
			break
		}
	}
	hasTools := len(job.Tools) > 0

	// Если тулзов нет, но эвристика говорит что нужны — подмешиваем из реестра
	if !hasTools && tools.ShouldActivate(lastText, true, "auto") {
		for _, t := range tools.DefaultRegistry.List() {
			job.Tools = append(job.Tools, translate.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
			hasTools = true
			if len(job.Tools) >= 4 { // не спамим
				break
			}
		}
		if hasTools && job.ToolChoice == "" {
			job.ToolChoice = "auto"
		}
	}

	if hasTools {
		// Конвертируем в tools.Tool для бриджа
		var bTools []tools.Tool
		for _, t := range job.Tools {
			var raw json.RawMessage
			if t.Parameters != nil {
				if b, err := json.Marshal(t.Parameters); err == nil {
					raw = b
				}
			}
			bTools = append(bTools, tools.Tool{Name: t.Name, Description: t.Description, Parameters: raw})
		}
		// Проверяем, нужно ли инжектить промпт
		if tools.ShouldActivate(lastText, hasTools, job.ToolChoice) {
			if prompt := tools.DefaultBridge.PromptInjection(bTools, job.ToolChoice); prompt != "" {
				job.System = append(job.System, prompt)
			}
		}
	}

	// 3. Окно контекста — режем старые терны если слишком много токенов
	// Оценка: 4 символа = 1 токен, лимит 16k токенов на историю (оставляем место для ответа)
	job.Turns = windowTurns(job.Turns, 16000)
}

func toToolRefs(tools []translate.Tool) []contextx.ToolRef {
	out := make([]contextx.ToolRef, 0, len(tools))
	for _, t := range tools {
		out = append(out, contextx.ToolRef{Name: t.Name, Desc: t.Description})
	}
	return out
}

func windowTurns(turns []translate.Turn, maxTokens int) []translate.Turn {
	// Используем contextx.Window с эстиматором
	cTurns := make([]contextx.Turn, len(turns))
	for i, t := range turns {
		cTurns[i] = contextx.Turn{Role: t.Role, Text: t.Text}
	}
	kept := contextx.Window(cTurns, maxTokens, func(s string) int {
		n := len(s) / 4
		if n == 0 && s != "" {
			n = 1
		}
		return n
	})
	out := make([]translate.Turn, len(kept))
	for i, t := range kept {
		out[i] = translate.Turn{Role: t.Role, Text: t.Text}
	}
	return out
}

// isToolResultMessage проверяет, это ли tool-результат (для контекста)
func isToolResultMessage(text string) bool {
	return strings.HasPrefix(text, "[tool output]") || strings.Contains(text, "tool_result")
}

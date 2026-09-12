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

	// Если тулзов нет — каждый тул из реестра САМ проверяет нужен ли он
	if !hasTools {
		var relevant []tools.Tool
		for _, t := range tools.DefaultRegistry.List() {
			// Каждый тул сам решает через ShouldActivate (проверяет имя/описание в тексте)
			if tools.ShouldActivate(lastText+" "+t.Name+" "+t.Description, true, "auto") {
				// Доп. фильтр: имя тулза должно хоть как-то резонировать с запросом
				// или запрос явно про тулзы/файлы. Иначе не спамим.
				lowText := strings.ToLower(lastText)
				lowName := strings.ToLower(t.Name)
				lowDesc := strings.ToLower(t.Description)
				if strings.Contains(lowText, lowName) || strings.Contains(lowText, "файл") || strings.Contains(lowText, "file") || strings.Contains(lowDesc, "file") && strings.Contains(lowText, "файл") {
					relevant = append(relevant, t)
				} else if strings.Contains(lowText, "tool") || strings.Contains(lowText, "тул") || strings.Contains(lowText, "вызов") {
					relevant = append(relevant, t)
				}
			}
			if len(relevant) >= 2 { // не спамим, максимум 2 авто-тулзы
				break
			}
		}
		// Если релевантных нет, но ShouldActivate говорит что нужны — возьмём первые 2 как fallback
		if len(relevant) == 0 && tools.ShouldActivate(lastText, true, "auto") {
			all := tools.DefaultRegistry.List()
			if len(all) > 2 {
				all = all[:2]
			}
			relevant = all
		}
		for _, t := range relevant {
			job.Tools = append(job.Tools, translate.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		}
		if len(relevant) > 0 {
			hasTools = true
			if job.ToolChoice == "" {
				job.ToolChoice = "auto"
			}
		}
	}

	if hasTools {
		// Конвертируем в tools.Tool для бриджа — каждый тул сам проверяет
		var bTools []tools.Tool
		for _, t := range job.Tools {
			// Сам тул проверяет свой контекст (имя/описание в тексте)
			if !tools.ShouldActivate(lastText+" "+t.Name, true, job.ToolChoice) {
				// Даже если один тул не нужен, оставляем — но промпт будет только для релевантных
				// Для простоты оставляем все, бридж сам отфильтрует по ShouldActivate
			}
			var raw json.RawMessage
			if t.Parameters != nil {
				if b, err := json.Marshal(t.Parameters); err == nil {
					raw = b
				}
			}
			bTools = append(bTools, tools.Tool{Name: t.Name, Description: t.Description, Parameters: raw})
		}
		// Промпт инжектится только если хотя бы один тул реально нужен.
		// Config-плейсмент: спеки едут в config-блоке транскрипта
		// (харнесс-нативный вид), в тексте — только протокол вызова.
		if job.ToolsPlacement == "config" {
			if spec := translate.ToolsSpecJSON(job.Tools); spec != "" {
				job.ToolsSpecJSON = spec
				job.System = append(job.System, translate.ConfigProtocolHint)
			} else if tools.ShouldActivate(lastText, hasTools, job.ToolChoice) {
				if prompt := tools.DefaultBridge.PromptInjection(bTools, job.ToolChoice); prompt != "" {
					job.System = append(job.System, prompt)
				}
			}
		} else if tools.ShouldActivate(lastText, hasTools, job.ToolChoice) {
			if prompt := tools.DefaultBridge.PromptInjection(bTools, job.ToolChoice); prompt != "" {
				job.System = append(job.System, prompt)
			}
		}
	}

	// 2b. Effort — явной строкой в system (честный хинт, не скрытый рероут).
	if line := translate.EffortInstruction(job.Effort); line != "" {
		job.System = append([]string{line}, job.System...)
	}

	// 3. Окно контекста — как у реальной модели (200k для Claude, 128k для GPT).
	// Раньше было 16k — теперь полный контекст. Оставляем 180k чтобы влез ответ.
	job.Turns = windowTurns(job.Turns, 180000)
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

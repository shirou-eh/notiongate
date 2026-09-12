package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Bridge — переводит OpenAI/Anthropic тулзы ↔ Notion промпт.
// Всегда может активироваться сам: если в запросе есть tools, вставляет
// инструкцию; если модель вернула JSON с tool_calls — парсит.
type Bridge struct {
	reg *Registry
}

func NewBridge(reg *Registry) *Bridge { return &Bridge{reg: reg} }

// PromptInjection возвращает системный кусок, который надо добавить
// когда есть тулзы. Немой — просто текст. Кап 10 тулзов, 8KB per params.
func (b *Bridge) PromptInjection(tools []Tool, choice string) string {
	if len(tools) == 0 {
		return ""
	}
	if len(tools) > 10 {
		tools = tools[:10]
	}
	var sb strings.Builder
	sb.WriteString("You have tools. To call a tool, output ONLY a JSON block:\n")
	sb.WriteString("```json\n{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"<name>\",\"arguments\":\"{...}\"}}]}\n```\n")
	sb.WriteString("Available tools:\n")
	for _, t := range tools {
		params := string(t.Parameters)
		if len(params) > 8192 {
			params = params[:8192] + "…(truncated)"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n  params: %s\n", t.Name, t.Description, params))
		if sb.Len() > 64*1024 {
			sb.WriteString("…(truncated)\n")
			break
		}
	}
	if choice == "required" || choice == "auto" {
		sb.WriteString("If needed, call a tool. Otherwise answer normally.\n")
	}
	if choice == "required" {
		sb.WriteString("You MUST call one of the tools now.\n")
	}
	if choice == "none" {
		return "" // явно запрещено
	}
	return sb.String()
}

// Call — распарсенный вызов
type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Extract пытается вытащить tool_calls из текста модели.
// Ищет ```json блок или голый JSON.
// Валидация имён — по реестру сервера (legacy). Для клиентских тулзов
// (агенты: OpenCode/Claude Code присылают свои Edit/Bash/Read/...) используй
// ExtractAllowed — иначе чужие тулзы будут молча отброшены и агент решит,
// что модель "не умеет в тулзы".
var reJSONBlock = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")
var reToolCalls = regexp.MustCompile(`"tool_calls"\s*:\s*\[`)

func (b *Bridge) Extract(text string) ([]Call, string, bool) {
	return b.ExtractAllowed(text, nil)
}

// ExtractAllowed — то же, но разрешённые имена берутся из запроса клиента.
// allowed == nil → проверка по реестру (старое поведение).
// allowed != nil (даже пустой) → разрешено всё из allowed + всё из реестра,
// а если allowed непустой и имя есть в allowed — принимаем даже если его нет
// в реестре. Это и есть passthrough клиентских тулзов: прокси не исполняет
// их сам (файлы правит агент на компе пользователя), а только честно
// передаёт tool_calls туда-обратно.
func (b *Bridge) ExtractAllowed(text string, allowed []string) ([]Call, string, bool) {
	if len(text) > 128*1024 {
		text = text[:128*1024]
	}
	// 1) блок ```json
	m := reJSONBlock.FindStringSubmatch(text)
	candidate := ""
	if len(m) == 2 {
		candidate = m[1]
	} else if reToolCalls.MatchString(text) {
		// попробуй найти первый { ... "tool_calls" ... }
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start >= 0 && end > start {
			candidate = text[start : end+1]
		}
	}
	if candidate == "" {
		return nil, text, false
	}
	if len(candidate) > 64*1024 {
		return nil, text, false
	}
	var wrapper struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(candidate), &wrapper); err != nil || len(wrapper.ToolCalls) == 0 {
		return nil, text, false
	}
	// валидируем имена: реестр + явно разрешённые клиентом.
	// allowed==nil → только реестр (legacy); иначе — объединение.
	allowSet := map[string]bool{}
	useAllowList := allowed != nil
	for _, n := range allowed {
		allowSet[n] = true
	}
	// валидируем имена
	var out []Call
	for i, c := range wrapper.ToolCalls {
		name := c.Function.Name
		if name == "" {
			continue
		}
		_, inReg := b.reg.Get(name)
		if !inReg && !(useAllowList && allowSet[name]) {
			// неизвестный тул — пропускаем, но не фолбэчим
			continue
		}
		args := string(c.Function.Arguments)
		// arguments может прийти объектом {...} — нормализуем к строке.
		// Пустой/null → "{}".
		trimmed := strings.TrimSpace(args)
		if trimmed == "" || trimmed == "null" {
			args = "{}"
		} else if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			// уже JSON — компактим, если бьётся, оставляем как есть
			var v any
			if json.Unmarshal([]byte(trimmed), &v) == nil {
				if b2, err := json.Marshal(v); err == nil {
					args = string(b2)
				} else {
					args = trimmed
				}
			} else {
				args = trimmed
			}
		} else {
			// голая строка без кавычек — заворачиваем в JSON-строку
			if b2, err := json.Marshal(trimmed); err == nil {
				_ = b2
				args = trimmed
			}
		}
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		out = append(out, Call{ID: id, Name: name, Arguments: args})
	}
	if len(out) == 0 {
		return nil, text, false
	}
	// вырезаем блок из текста — остаётся чистый ответ
	// FIX: проверяем len(m) до доступа к m[0] (паника на bare JSON)
	var clean string
	if len(m) == 2 {
		clean = strings.Replace(text, m[0], "", 1)
	} else {
		clean = strings.Replace(text, candidate, "", 1)
	}
	return out, strings.TrimSpace(clean), true
}

// ShouldActivate — простая эвристика: если last user message содержит
// триггерные слова или tools не пустые и choice != none — активируем.
func ShouldActivate(text string, hasTools bool, choice string) bool {
	if !hasTools || choice == "none" {
		return false
	}
	if choice == "required" || choice == "auto" {
		return true
	}
	low := strings.ToLower(text)
	triggers := []string{"вызови", "вызов", "tool", "функци", "найди", "поиск", "открой", "создай", "удали", "запусти"}
	for _, t := range triggers {
		if strings.Contains(low, t) {
			return true
		}
	}
	return hasTools // по умолчанию если тулзы есть — даём шанс
}

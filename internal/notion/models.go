package notion

// KnownModels maps client-facing names to Notion-internal model codenames.
//
// Источник правды: POST /api/v3/getAvailableModels {spaceId} — живой ответ
// Notion (см. internal/notion/available_models.go). Таблица ниже — проверенный
// снимок этого ответа (19 enabled + 1 disabled Fable 5), нужен только как
// fallback когда пул пуст или discovery не сработал.
//
// ВАЖНО: здесь нет выдуманных алиасов. Каждая запись — 1:1 к реальному
// codename из Notion. Неизвестные имена НЕ мапятся на дефолт молча — API
// отвечает 400 model_not_found (см. pool.LookupModel).
var KnownModels = map[string]string{
	// OpenAI
	"gpt-5.2":      "oatmeal-cookie",
	"gpt-5.4":      "oval-kumquat-medium",
	"gpt-5.5":      "opal-quince-medium",
	"gpt-5.4-mini": "oregon-grape-medium",
	"gpt-5.4-nano": "otaheite-apple-medium",
	// Anthropic
	"sonnet-4.6": "almond-croissant-low",
	"opus-4.6":   "avocado-froyo-medium",
	"opus-4.7":   "apricot-sorbet-high",
	"opus-4.8":   "ambrosia-tart-high",
	"haiku-4.5":  "anthropic-haiku-4.5",
	// Gemini
	"gemini-2.5-flash": "vertex-gemini-2.5-flash",
	"gemini-3.5-flash": "vertex-gemini-3.5-flash",
	"gemini-3.1-pro":   "galette-medium-thinking",
	"gemini-3-flash":   "gingerbread",
	// Mystery / third-party
	"kimi-k2.6":       "fireworks-kimi-k2.6",
	"deepseek-v4-pro": "baseten-deepseek-v4-pro",
	"glm-5.2":         "baseten-glm-5.2",
	"grok-4.3":        "xigua-mochi-medium",
	"grok-build-0.1":  "xinomavro-cake",
	// Disabled на большинстве планов (business/enterprise only) — маппинг
	// оставлен чтобы запрос возвращал честную upstream-ошибку, а не 400.
	// В /v1/models disabled НЕ показывается.
	"fable-5": "acai-budino-high",

	// Совместимые префиксы (те же codename, без выдумок):
	"claude-sonnet-4.6": "almond-croissant-low",
	"claude-opus-4.6":   "avocado-froyo-medium",
	"claude-opus-4.7":   "apricot-sorbet-high",
	"claude-opus-4.8":   "ambrosia-tart-high",
	"claude-haiku-4.5":  "anthropic-haiku-4.5",
}

// DisabledModels — client-facing имена, которые Notion отдаёт с
// isDisabled=true (план business/enterprise required). В /v1/models они
// скрыты, но LookupModel их знает чтобы вернуть честную upstream-ошибку.
var DisabledModels = map[string]bool{
	"fable-5": true,
}

// DefaultModel используется только когда клиент вообще не указал model
// (пустая строка). Неизвестное имя — это 400, а не silent fallback.
const DefaultModel = "almond-croissant-low"

// codenameSet — все известные внутренние codename (значения KnownModels).
func codenameSet() map[string]bool {
	out := make(map[string]bool, len(KnownModels))
	for _, v := range KnownModels {
		out[v] = true
	}
	return out
}

// IsCodename reports whether s is a known Notion-internal codename.
func IsCodename(s string) bool {
	_, ok := codenameSet()[s]
	return ok
}

// EnabledFriendlyNames возвращает client-facing имена без disabled
// (для /v1/models fallback). Отсортировано для детерминизма.
func EnabledFriendlyNames() []string {
	out := make([]string, 0, len(KnownModels))
	for k := range KnownModels {
		if !DisabledModels[k] {
			out = append(out, k)
		}
	}
	return out
}

// FriendlyForCodename возвращает основное client-facing имя для codename.
// Если codename неизвестен таблице (новая модель из getAvailableModels),
// возвращается сам codename — новая модель сразу доступна без обновления.
func FriendlyForCodename(codename string) string {
	best := ""
	for k, v := range KnownModels {
		if v != codename || DisabledModels[k] {
			continue
		}
		if best == "" || len(k) < len(best) || (len(k) == len(best) && k < best) {
			best = k
		}
	}
	if best != "" {
		return best
	}
	return codename
}

// ModelList returns the known client-facing model names (legacy name kept).
func ModelList() []string {
	return EnabledFriendlyNames()
}

// AllModelIDs returns every client-facing model name (sorted for determinism).
func AllModelIDs() []string {
	out := EnabledFriendlyNames()
	seen := map[string]bool{}
	for _, k := range out {
		seen[k] = true
	}
	for _, v := range KnownModels {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}

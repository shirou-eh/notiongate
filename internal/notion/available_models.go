package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// AvailableModel — одна запись из POST /api/v3/getAvailableModels.
//
// Реальный формат (проверен по живому ответу Notion):
//
//	{
//	  "models": [
//	    {"model":"oatmeal-cookie","modelMessage":"GPT-5.2","modelFamily":"openai",
//	     "isDisabled":false, ...},
//	    {"model":"acai-budino-high","modelMessage":"Fable 5",
//	     "isDisabled":true,"disabledReason":"business_or_enterprise_plan_required", ...}
//	  ],
//	  "restrictedAccessModelsInPickerConfig": [...]
//	}
//
// Доступ зависит от workspace/плана/feature flags, поэтому список запрашиваем
// per-space и per-account, а не хардкодим.
type AvailableModel struct {
	Codename       string `json:"codename"`
	DisplayName    string `json:"display_name"`
	Family         string `json:"family,omitempty"`
	Disabled       bool   `json:"disabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}

// Enabled возвращает только доступные модели.
func EnabledModels(in []AvailableModel) []AvailableModel {
	out := make([]AvailableModel, 0, len(in))
	for _, m := range in {
		if m.Codename != "" && !m.Disabled {
			out = append(out, m)
		}
	}
	return out
}

// EnabledCodenames — удобный вид для bootstrap/логов.
func EnabledCodenames(in []AvailableModel) []string {
	en := EnabledModels(in)
	out := make([]string, 0, len(en))
	for _, m := range en {
		out = append(out, m.Codename)
	}
	sort.Strings(out)
	return out
}

// FriendlyModelList маппит живой ответ getAvailableModels в client-facing
// имена: только enabled; известным codename — friendly slug, новым
// (которых ещё нет в KnownModels) — сам codename как есть, чтобы новая
// модель была доступна сразу без обновления прокси. Codename тоже хранится
// как алиас чтобы запрос по codename резолвился.
func FriendlyModelList(avail []AvailableModel) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range avail {
		if m.Codename == "" || m.Disabled {
			continue
		}
		name := FriendlyForCodename(m.Codename)
		if name == "" {
			name = m.Codename
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		if m.Codename != name && !seen[m.Codename] {
			seen[m.Codename] = true
			out = append(out, m.Codename)
		}
	}
	return out
}

// GetAvailableModels запрашивает у Notion список моделей для space.
// spaceID обязателен — без него Notion отвечает пусто/ошибкой.
func (c *Client) GetAvailableModels(ctx context.Context, spaceID string) ([]AvailableModel, error) {
	if spaceID == "" {
		return nil, fmt.Errorf("getAvailableModels: spaceID is required")
	}
	payload, _ := json.Marshal(map[string]any{"spaceId": spaceID})
	raw, err := c.postJSON(ctx, "getAvailableModels", payload)
	if err != nil && c.base != "" {
		if alt := flipDomain(c.base); alt != "" {
			orig := c.base
			c.base = alt
			raw2, err2 := c.postJSON(ctx, "getAvailableModels", payload)
			if err2 != nil {
				c.base = orig
				return nil, err
			}
			raw = raw2
		}
	} else if err != nil {
		return nil, err
	}
	models, err := parseAvailableModels(raw)
	if err != nil {
		return nil, err
	}
	return models, nil
}

// parseAvailableModels tolerantно разбирает ответ Notion.
// Поддержаны формы:
//
//	{"models":[{...}]}, {"data":[{...}]}, [{...}], {"<id>":{...}}
//
// В каждой записи codename ищется в полях model/id/codename,
// display — в modelMessage/displayName/name/label,
// disabled — в isDisabled/disabled.
func parseAvailableModels(raw []byte) ([]AvailableModel, error) {
	var top any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("getAvailableModels: unexpected response shape")
	}
	var items []any
	switch v := top.(type) {
	case []any:
		items = v
	case map[string]any:
		if arr, ok := v["models"].([]any); ok {
			items = arr
		} else if arr, ok := v["data"].([]any); ok {
			items = arr
		} else {
			// Fallback: собрать все вложенные объекты с полем model/id
			items = collectModelObjects(v)
		}
	default:
		return nil, fmt.Errorf("getAvailableModels: unexpected response shape")
	}
	seen := map[string]bool{}
	out := make([]AvailableModel, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		codename := strField(m, "model", "id", "codename", "finalModelName")
		if codename == "" {
			continue
		}
		// restrictedAccess-записи (acai-budino без -high) — не модели,
		// пропускаем чтобы не плодить фейковые id.
		display := strField(m, "modelMessage", "displayName", "name", "label")
		family := strField(m, "modelFamily", "family")
		disabled := boolField(m, "isDisabled", "disabled")
		reason, _ := m["disabledReason"].(string)
		if seen[codename] {
			continue
		}
		seen[codename] = true
		out = append(out, AvailableModel{
			Codename:       codename,
			DisplayName:    display,
			Family:         family,
			Disabled:       disabled,
			DisabledReason: reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Codename < out[j].Codename })
	return out, nil
}

func collectModelObjects(v map[string]any) []any {
	var out []any
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if _, ok := t["model"]; ok {
				out = append(out, t)
			} else if _, ok := t["codename"]; ok {
				out = append(out, t)
			} else {
				for _, e := range t {
					walk(e)
				}
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func boolField(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if b, ok := m[k].(bool); ok && b {
			return true
		}
	}
	return false
}

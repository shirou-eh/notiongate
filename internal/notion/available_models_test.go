package notion

import (
	"testing"
)

func TestParseAvailableModelsRealShape(t *testing.T) {
	raw := []byte(`{"models":[
		{"model":"oatmeal-cookie","modelMessage":"GPT-5.2","modelFamily":"openai","isDisabled":false},
		{"model":"acai-budino-high","modelMessage":"Fable 5","modelFamily":"anthropic","isDisabled":true,"disabledReason":"business_or_enterprise_plan_required"},
		{"id":"e2e-mini"}
	]}`)
	got, err := parseAvailableModels(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	en := EnabledModels(got)
	if len(en) != 2 {
		t.Fatalf("enabled = %d, want 2", len(en))
	}
	if en[0].Codename != "e2e-mini" && en[0].Codename != "oatmeal-cookie" {
		t.Fatalf("unexpected first enabled: %+v", en[0])
	}
	// Disabled отфильтрован, но распарсен с причиной.
	foundDisabled := false
	for _, m := range got {
		if m.Codename == "acai-budino-high" && m.Disabled && m.DisabledReason != "" {
			foundDisabled = true
		}
	}
	if !foundDisabled {
		t.Fatalf("disabled Fable 5 not parsed: %+v", got)
	}
	// friendly mapping для известных codename.
	if FriendlyForCodename("oatmeal-cookie") != "gpt-5.2" {
		t.Fatalf("friendly = %q", FriendlyForCodename("oatmeal-cookie"))
	}
	// Новый неизвестный codename — как есть (доступен сразу).
	if FriendlyForCodename("fireworks-future-model-9.9") != "fireworks-future-model-9.9" {
		t.Fatalf("unknown codename must passthrough")
	}
	// Новые модели из живого каталога 2026-09-12 резолвятся.
	if FriendlyForCodename("fireworks-kimi-k3") != "kimi-k3" {
		t.Fatalf("kimi-k3 friendly = %q", FriendlyForCodename("fireworks-kimi-k3"))
	}
	if FriendlyForCodename("angel-cake-high") != "sonnet-5" {
		t.Fatalf("sonnet-5 friendly = %q", FriendlyForCodename("angel-cake-high"))
	}
	if FriendlyForCodename("grapefruit-zeppole") != "gemini-3.7-flash" {
		t.Fatalf("gemini-3.7-flash friendly = %q", FriendlyForCodename("grapefruit-zeppole"))
	}
}

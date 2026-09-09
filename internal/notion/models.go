package notion

// KnownModels maps client-facing names to Notion-internal model codenames
// (recovered from the 2026 web app; Notion rotates these occasionally —
// unknown names are passed through to the server default).
var KnownModels = map[string]string{
	"claude-opus-4.6":   "avocado-froyo-medium",
	"claude-opus-4.7":   "apricot-sorbet-high",
	"claude-opus-4.8":   "ambrosia-tart-high",
	"claude-sonnet-4.6": "almond-croissant-low",
	"claude-sonnet-4.5": "almond-croissant-low",
	"gemini-2.5-flash":  "vertex-gemini-2.5-flash",
	"gemini-3.1-pro":    "galette-medium-thinking",
	"gpt-5.2":           "oatmeal-cookie",
	"gpt-5.4":           "oval-kumquat-medium",
	"gpt-5.5":           "opal-quince-medium",
	"kimi-2.6":          "fireworks-kimi-k2.6",
}

// DefaultModel is used when a request names an unknown model.
const DefaultModel = "almond-croissant-low"

// ModelList returns the known model codenames.
func ModelList() []string {
	out := make([]string, 0, len(KnownModels))
	for _, id := range KnownModels {
		out = append(out, id)
	}
	return out
}

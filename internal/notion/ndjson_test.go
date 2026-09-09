package notion

import (
	"context"
	"strings"
	"testing"
)

func runParser(t *testing.T, input string) []Event {
	t.Helper()
	p := &streamParser{}
	out := make(chan Event, 256)
	ctx := context.Background()
	if err := p.consume(ctx, strings.NewReader(input), out); err != nil {
		t.Fatalf("consume: %v", err)
	}
	p.emitFinal(out, ctx)
	close(out)
	var evs []Event
	for e := range out {
		evs = append(evs, e)
	}
	return evs
}

// Shapes follow the live-validated 2026 patch protocol.
const patchStream = `
{"type":"patch-start","data":{"s":[{"id":"a","type":"config"},{"id":"b","type":"context"}]}}
{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"c1","type":"agent-inference","value":[{"type":"thinking","content":"hmm"},{"type":"text","content":"Hel"}]}}]}
{"type":"patch","v":[{"o":"a","p":"/s/2/value/-","v":{"type":"text","content":"lo"}}]}
{"type":"patch","v":[{"o":"x","p":"/s/2/value/1/content","v":" world"}]}
{"type":"patch","v":[{"o":"a","p":"/s/2/inputTokens","v":120},{"o":"a","p":"/s/2/outputTokens","v":34}]}
`

func TestPatchStreamTextThinkingUsage(t *testing.T) {
	evs := runParser(t, patchStream)
	var text, reasoning strings.Builder
	var inTok, outTok int64
	for _, e := range evs {
		switch e.Kind {
		case EventText:
			text.WriteString(e.Text)
		case EventReasoning:
			reasoning.WriteString(e.Text)
		case EventUsage:
			inTok, outTok = e.TokensIn, e.TokensOut
		}
	}
	if got := text.String(); got != "Hello world" {
		t.Fatalf("text = %q, want %q", got, "Hello world")
	}
	if got := reasoning.String(); got != "hmm" {
		t.Fatalf("reasoning = %q", got)
	}
	if inTok != 120 || outTok != 34 {
		t.Fatalf("usage = %d/%d", inTok, outTok)
	}
}

// Inline section absorption: /s/- with value carrying the entire turn.
const inlineStream = `
{"type":"patch-start","data":{"s":[]}}
{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"s1","type":"agent-inference","value":[{"type":"text","content":"short reply"}]}}]}
`

func TestPatchStreamInlineSection(t *testing.T) {
	evs := runParser(t, inlineStream)
	if FullText(evs) != "short reply" {
		t.Fatalf("text = %q", FullText(evs))
	}
}

// Cumulative mode (asPatchResponse=false).
const cumulativeStream = `
{"type":"agent-inference","value":[{"type":"text","content":"Hel"}]}
{"type":"agent-inference","value":[{"type":"text","content":"Hello"},{"type":"thinking","content":"h"}]}
`

func TestCumulativeAgentInference(t *testing.T) {
	evs := runParser(t, cumulativeStream)
	var text, reasoning strings.Builder
	for _, e := range evs {
		switch e.Kind {
		case EventText:
			text.WriteString(e.Text)
		case EventReasoning:
			reasoning.WriteString(e.Text)
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q, want delta %q", text.String(), "Hello")
	}
	if reasoning.String() != "h" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
}

func TestPatchErrorSegment(t *testing.T) {
	in := `{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"e1","type":"error","message":"Agent inference failed: aiNotEnabled (AiNotEnabledOnSpaceGetCompletionError)"}}]}`
	evs := runParser(t, in)
	if len(evs) != 1 || evs[0].Kind != EventError {
		t.Fatalf("expected single error event, got %+v", evs)
	}
	if evs[0].Err == nil || !strings.Contains(errText(evs[0]), "aiNotEnabled") {
		t.Fatalf("error text wrong: %v", evs[0].Err)
	}
}

func TestTopLevelErrorTerminal(t *testing.T) {
	evs := runParser(t, `{"type":"error","message":"boom"}`+"\n"+`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"agent-inference","value":[{"type":"text","content":"nope"}]}}]}`)
	if len(evs) != 1 || evs[0].Kind != EventError {
		t.Fatalf("expected terminal error only, got %+v", evs)
	}
}

func TestToolUseIgnored(t *testing.T) {
	in := `
{"type":"patch-start","data":{"s":[]}}
{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"t1","type":"agent-tool-result","toolName":"view","result":{"entities":[]}}}]}
{"type":"patch","v":[{"o":"a","p":"/s/0/value/-","v":{"type":"tool_use","content":"internal tool text"}}]}
`
	evs := runParser(t, in)
	if FullText(evs) != "" {
		t.Fatalf("tool_use leaked into text: %q", FullText(evs))
	}
}

func TestLangMarkupCleaned(t *testing.T) {
	in := `
{"type":"patch-start","data":{"s":[]}}
{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"id":"s1","type":"agent-inference","value":[{"type":"text","content":"<lang primary=\"ru-RU\">Привет</lang>!"}]}}]}
{"type":"patch","v":[{"o":"x","p":"/s/0/value/0/content","v":"<lang primary=\"en\">x</lang>"}]}
`
	evs := runParser(t, in)
	if got := FullText(evs); got != "Привет!x" {
		t.Fatalf("text = %q", got)
	}
}

// getSpacesV2: nested 2026 format with two workspaces.
const nestedGetSpaces = `{
  "3d2d872b-594c-81f3-ae2b-0002fa1534da": {
    "__version__": 3,
    "notion_user": {"3d2d872b-594c-81f3-ae2b-0002fa1534da": {"value": {"value": {"email": "cookie@test.io", "id": "3d2d872b-594c-81f3-ae2b-0002fa1534da"}}}},
    "space_view": {
      "3d2bdc68-f2da-8170-a1cb-0006caadee22": {"spaceId": "dfbbdc68-f2da-8156-bbae-0003585ede17"},
      "3d31de67-b3de-81d5-8b39-00065b70ab7d": {"spaceId": "29b1de67-b3de-81b7-8352-0003977a3573"}
    },
    "space": {
      "dfbbdc68-f2da-8156-bbae-0003585ede17": {"spaceId": "dfbbdc68-f2da-8156-bbae-0003585ede17", "value": {"value": {"id": "dfbbdc68-f2da-8156-bbae-0003585ede17", "name": "Cookie's Space", "plan_type": "team", "subscription_tier": "business", "settings": {"crdt_status": "on"}}}},
      "29b1de67-b3de-81b7-8352-0003977a3573": {"spaceId": "29b1de67-b3de-81b7-8352-0003977a3573", "value": {"value": {"id": "29b1de67-b3de-81b7-8352-0003977a3573", "name": "Cookie's Space", "plan_type": "team", "subscription_tier": "business", "settings": {"enable_ai_feature": true}}}}
    }
  }
}`

func TestGetSpacesV2Nested(t *testing.T) {
	var c Client
	uid, email, spaces, err := c.getSpacesV2Raw([]byte(nestedGetSpaces))
	if err != nil {
		t.Fatal(err)
	}
	if uid != "3d2d872b-594c-81f3-ae2b-0002fa1534da" {
		t.Fatalf("uid = %q", uid)
	}
	if email != "cookie@test.io" {
		t.Fatalf("email = %q", email)
	}
	if len(spaces) != 2 {
		t.Fatalf("spaces = %+v", spaces)
	}
	byID := map[string]SpaceInfo{}
	for _, s := range spaces {
		byID[s.ID] = s
	}
	a := byID["dfbbdc68-f2da-8156-bbae-0003585ede17"]
	if a.SpaceViewID != "3d2bdc68-f2da-8170-a1cb-0006caadee22" || a.AIEnabledFlag {
		t.Fatalf("space a wrong: %+v", a)
	}
	b := byID["29b1de67-b3de-81b7-8352-0003977a3573"]
	if b.SpaceViewID != "3d31de67-b3de-81d5-8b39-00065b70ab7d" || !b.AIEnabledFlag {
		t.Fatalf("space b wrong: %+v", b)
	}
}

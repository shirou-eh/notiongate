package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SpaceInfo describes one workspace of an account.
type SpaceInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SpaceViewID      string `json:"space_view_id,omitempty"`
	PlanType         string `json:"plan_type,omitempty"`
	SubscriptionTier string `json:"subscription_tier,omitempty"`
	AIEnabledFlag    bool   `json:"ai_enabled_flag"` // settings.enable_ai_feature
}

// BootstrapInfo is everything the proxy auto-discovers from a token_v2 cookie.
type BootstrapInfo struct {
	UserID    string      `json:"user_id"`
	Email     string      `json:"email,omitempty"`
	SpaceID   string      `json:"space_id"`
	SpaceName string      `json:"space_name,omitempty"`
	SpaceViewID string    `json:"space_view_id,omitempty"`
	Models    []string    `json:"models,omitempty"`
	BaseURL   string      `json:"base_url,omitempty"` // upstream base that actually worked
	Spaces    []SpaceInfo `json:"spaces,omitempty"`   // all discovered workspaces
}

// getSpacesV2Raw parses a raw getSpaces response (exported for tests).
func (c *Client) getSpacesV2Raw(raw []byte) (userID, email string, spaces []SpaceInfo, err error) {
	return parseSpacesV2(raw)
}

// getSpacesV2 calls getSpaces and parses the nested response format used by
// the current Notion web app:
//
//	{"<user_id>": {"space": {"<space_id>": {"spaceId":..., "value": {"value": {...record...}}}},
//	                "space_view": {"<view_id>": {"spaceId":..., ...}},
//	                "notion_user": {"<user_id>": {"value": {"value": {...}}}}}
//
// The legacy array format (older responses) is handled as a fallback.
func (c *Client) getSpacesV2(ctx context.Context) (userID, email string, spaces []SpaceInfo, err error) {
	raw, err := c.postJSON(ctx, "getSpaces", []byte("{}"))
	if err != nil {
		return "", "", nil, err
	}
	return parseSpacesV2(raw)
}

func parseSpacesV2(raw []byte) (userID, email string, spaces []SpaceInfo, err error) {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return "", "", nil, fmt.Errorf("getSpaces: unexpected response shape")
	}

	if len(obj) == 0 {
		return "", "", nil, fmt.Errorf("getSpaces: empty response")
	}

	// Nested format: there is one top-level key per user id.
	for topKey, topVal := range obj {
		top, ok := topVal.(map[string]any)
		if !ok {
			continue
		}
		if userID == "" && looksLikeUserID(topKey) {
			userID = topKey
		}
		// notion_user → user id + email
		if nu, ok := top["notion_user"].(map[string]any); ok {
			for id, v := range nu {
				if userID == "" && id != "" {
					userID = id
				}
				e := digEmail(v)
				if e != "" {
					email = e
				}
			}
		}
		// space_view → map spaceId → viewId
		viewBySpace := map[string]string{}
		if sv, ok := top["space_view"].(map[string]any); ok {
			for viewID, v := range sv {
				if m, ok := v.(map[string]any); ok {
					if sid, ok := m["spaceId"].(string); ok && sid != "" {
						viewBySpace[sid] = viewID
					}
				}
			}
		}
		// space records
		if sp, ok := top["space"].(map[string]any); ok {
			for sid, v := range sp {
				rec := digValueRecord(v)
				if rec == nil {
					continue
				}
				si := SpaceInfo{ID: sid}
				si.Name, _ = rec["name"].(string)
				si.PlanType, _ = rec["plan_type"].(string)
				si.SubscriptionTier, _ = rec["subscription_tier"].(string)
				si.SpaceViewID = viewBySpace[sid]
				if settings, ok := rec["settings"].(map[string]any); ok {
					si.AIEnabledFlag, _ = settings["enable_ai_feature"].(bool)
				}
				spaces = append(spaces, si)
			}
		}
	}
	if userID == "" {
		userID = digUserID(obj)
	}
	return userID, email, spaces, nil
}

func looksLikeUserID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

// digValueRecord unwraps {"value": {"value": {...record...}}} (and single wraps).
func digValueRecord(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cur := m
	for i := 0; i < 4; i++ {
		inner, ok := cur["value"].(map[string]any)
		if !ok {
			break
		}
		cur = inner
	}
	if _, ok := cur["name"]; ok || len(cur) > 2 {
		return cur
	}
	return cur
}

func digEmail(v any) string {
	rec := digValueRecord(v)
	if rec == nil {
		return ""
	}
	e, _ := rec["email"].(string)
	return e
}

func digUserID(obj map[string]any) string {
	for k, v := range obj {
		if !looksLikeUserID(k) {
			continue
		}
		top, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if nu, ok := top["notion_user"].(map[string]any); ok {
			for id := range nu {
				if looksLikeUserID(id) {
					return id
				}
			}
		}
		_ = k
	}
	return ""
}

// Bootstrap discovers user id, workspaces and the AI-usable space for a token.
//
// Space selection follows a probe-first strategy: each space gets a tiny real
// inference probe; the first space whose stream produces content (and no
// aiNotEnabled error) is selected. Spaces answering with aiNotEnabled are
// skipped, silently-empty spaces are kept only as a last resort.
func (c *Client) Bootstrap(ctx context.Context) (*BootstrapInfo, error) {
	info := &BootstrapInfo{}

	// Primary + alternate host probing for the whole discovery.
	userID, email, spaces, err := c.getSpacesV2(ctx)
	if err != nil && c.base != "" {
		if alt := flipDomain(c.base); alt != "" {
			origBase := c.base
			c.base = alt
			userID2, email2, spaces2, err2 := c.getSpacesV2(ctx)
			if err2 != nil {
				c.base = origBase
				return nil, err
			}
			userID, email, spaces = userID2, email2, spaces2
			info.BaseURL = alt
		} else {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	info.UserID = userID
	info.Email = email
	info.Spaces = spaces
	if info.BaseURL == "" {
		info.BaseURL = c.base
	}
	if info.UserID == "" || len(spaces) == 0 {
		return info, fmt.Errorf("bootstrap incomplete: user_id=%q spaces=%d (token may be invalid)", info.UserID, len(spaces))
	}

	// Probe spaces for usable AI.
	space, kind := c.pickWorkingSpace(ctx, info, spaces)
	switch kind {
	case spaceOK:
		info.SpaceID = space.ID
		info.SpaceName = space.Name
		info.SpaceViewID = space.SpaceViewID
	case spaceUnknown:
		// All probes were inconclusive (throttled?) — fall back to the first
		// space so the account can still be added; a fake model must not be
		// invented (the pool falls back to the built-in codename table).
		info.SpaceID = space.ID
		info.SpaceName = space.Name
		info.SpaceViewID = space.SpaceViewID
	default:
		return info, fmt.Errorf("no AI-enabled space found (all probed spaces answered aiNotEnabled or empty)")
	}
	return info, nil
}

type spaceProbeKind int

const (
	spaceOK spaceProbeKind = iota
	spaceAIDisabled
	spaceUnknown
)

// pickWorkingSpace probes spaces and returns (best, kind).
func (c *Client) pickWorkingSpace(ctx context.Context, info *BootstrapInfo, spaces []SpaceInfo) (SpaceInfo, spaceProbeKind) {
	var unknown []SpaceInfo
	var disabled []SpaceInfo
	for _, s := range spaces {
		kind := c.probeSpace(ctx, info.UserID, s)
		switch kind {
		case spaceOK:
			return s, spaceOK
		case spaceAIDisabled:
			disabled = append(disabled, s)
		default:
			unknown = append(unknown, s)
		}
	}
	// No space produced content: prefer unknown (possibly throttled probes)
	// over explicitly aiNotEnabled-answered spaces, preserving Notion's
	// original enumeration order. Per field experience the enable_ai_feature
	// flag can be inverted, so it must NOT bump priority.
	if len(unknown) > 0 {
		return unknown[0], spaceUnknown
	}
	if len(disabled) > 0 {
		return disabled[0], spaceAIDisabled
	}
	if len(spaces) > 0 {
		return spaces[0], spaceUnknown
	}
	return SpaceInfo{}, spaceUnknown
}

// probeSpace sends a minimal real inference and classifies the answer.
func (c *Client) probeSpace(ctx context.Context, userID string, s SpaceInfo) spaceProbeKind {
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req := &InferenceRequest{
		SpaceID:      s.ID,
		UserID:       userID,
		SpaceViewID:  s.SpaceViewID,
		SpaceName:    s.Name,
		Transcript:   []TranscriptEntry{UserBlock("Reply with exactly: ok")},
		IsProbe:      true,
	}
	events, cancel2, err := c.RunInferenceStream(pctx, req)
	if err != nil {
		return spaceUnknown
	}
	defer cancel2()
	gotContent := false
	sawAINotEnabled := false
	bytes := 0
	for ev := range events {
		switch ev.Kind {
		case EventText:
			gotContent = true
			bytes += len(ev.Text)
		case EventError:
			if strings.Contains(errText(ev), "aiNotEnabled") || strings.Contains(errText(ev), "AiNotEnabled") {
				sawAINotEnabled = true
			}
		}
		if sawAINotEnabled {
			break
		}
	}
	if sawAINotEnabled {
		return spaceAIDisabled
	}
	if gotContent && bytes >= 1 {
		return spaceOK
	}
	return spaceUnknown
}

func errText(e Event) string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

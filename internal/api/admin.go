package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/plugin"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/store"
)

const adminBodyLimit = 1 << 20

// handleAdminStatus implements GET /admin/status.
func (s *Server) handleAdminStatus(w http.ResponseWriter, _ *http.Request) {
	views := s.pool.Snapshot()
	counts := map[string]int{}
	for _, v := range views {
		counts[v.EffectiveStatus]++
	}
	writeJSONRaw(w, http.StatusOK, map[string]any{
		"accounts":        len(views),
		"by_status":       counts,
		"rotate_at":       s.cfg.RotateAt,
		"default_window":  s.cfg.DefaultWindow,
		"default_limit":   s.cfg.DefaultLimit,
		"sticky_sessions": s.cfg.StickySessions,
		"max_attempts":    s.cfg.MaxAttempts,
		"pool":            views,
	})
}

// handleAdminStats implements GET /admin/stats.
func (s *Server) handleAdminStats(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	today, _ := s.st.StatsSince(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
	h24, _ := s.st.StatsSince(now.Add(-24 * time.Hour))
	d30, _ := s.st.StatsSince(now.Add(-30 * 24 * time.Hour))
	writeJSONRaw(w, http.StatusOK, map[string]any{
		"today": today, "last_24h": h24, "last_30d": d30,
	})
}

// handleAdminList implements GET /admin/accounts.
func (s *Server) handleAdminList(w http.ResponseWriter, _ *http.Request) {
	writeJSONRaw(w, http.StatusOK, map[string]any{"accounts": s.pool.Snapshot()})
}

// adminAddRequest is the POST /admin/accounts body.
type adminAddRequest struct {
	pool.AddInput
}

// handleAdminAdd implements POST /admin/accounts: bootstrap + add.
func (s *Server) handleAdminAdd(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, adminBodyLimit, false)
	if !ok {
		return
	}
	var req adminAddRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "invalid_body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	acc, err := s.pool.AddWithBootstrap(ctx, req.AddInput)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "bootstrap_failed")
		return
	}
	writeJSONRaw(w, http.StatusCreated, acc)
}

// handleAdminPatch implements PATCH /admin/accounts/{id}.
func (s *Server) handleAdminPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, ok := readBody(w, r, adminBodyLimit, false)
	if !ok {
		return
	}
	var req struct {
		Label      *string  `json:"label"`
		Proxy      *string  `json:"proxy"`
		LimitReq   *int64   `json:"limit_req"`
		WindowType *string  `json:"window_type"`
		RotateAt   *float64 `json:"rotate_at"`
		Status     *string  `json:"status"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "invalid_body")
		return
	}
	err := s.pool.Update(id, func(a *model.Account) {
		if req.Label != nil {
			a.Label = sanitizeLabel(*req.Label)
		}
		if req.Proxy != nil {
			a.Proxy = *req.Proxy
		}
		if req.LimitReq != nil {
			a.LimitReq = *req.LimitReq
		}
		if req.WindowType != nil {
			if *req.WindowType == model.WindowDay || *req.WindowType == model.WindowMonth {
				a.WindowType = *req.WindowType
			}
		}
		if req.RotateAt != nil && *req.RotateAt > 0 && *req.RotateAt <= 1 {
			a.RotateAt = *req.RotateAt
		}
		if req.Status != nil {
			switch strings.ToLower(*req.Status) {
			case "disable", "disabled":
				a.Status = model.StatusDisabled
			case "enable", "active":
				a.Status = model.StatusActive
				a.CooldownUntil = time.Time{}
				a.LastError = ""
			case "invalid":
				a.Status = model.StatusInvalid
			}
		}
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account not found", "invalid_request_error", "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "update_failed")
		return
	}
	acc, err := s.pool.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found", "invalid_request_error", "not_found")
		return
	}
	writeJSONRaw(w, http.StatusOK, acc)
}

// handleAdminDelete implements DELETE /admin/accounts/{id}.
func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.pool.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "account not found", "invalid_request_error", "not_found")
		return
	}
	writeJSONRaw(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleAdminTest implements POST /admin/accounts/{id}/test.
func (s *Server) handleAdminTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.pool.TestAccount(ctx, r.PathValue("id"))
	resp := map[string]any{"ok": err == nil}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSONRaw(w, http.StatusOK, resp)
}

// handlePluginsList implements GET /admin/plugins.
func (s *Server) handlePluginsList(w http.ResponseWriter, _ *http.Request) {
	list := plugin.List()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		caps := []string{}
		if _, ok := p.(plugin.AutoregProvider); ok {
			caps = append(caps, "autoreg")
		}
		if _, ok := p.(plugin.HTTPProvider); ok {
			caps = append(caps, "http")
		}
		if _, ok := p.(plugin.HookProvider); ok {
			caps = append(caps, "hooks")
		}
		out = append(out, map[string]any{
			"name":        p.Name(),
			"description": p.Description(),
			"version":     p.Version(),
			"caps":        caps,
		})
	}
	writeJSONRaw(w, http.StatusOK, map[string]any{"plugins": out})
}

var autoregMu sync.Mutex

func sanitizeLabel(s string) string {
	// Strip HTML tags and control chars, limit to 64 chars
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	// Remove < > to prevent stored XSS if admin UI uses innerHTML
	s = strings.ReplaceAll(s, "<", "")
	s = strings.ReplaceAll(s, ">", "")
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "'", "")
	return s
}

// handleAutoreg implements POST /admin/autoreg.
func (s *Server) handleAutoreg(w http.ResponseWriter, r *http.Request) {
	if !autoregMu.TryLock() {
		writeError(w, http.StatusTooManyRequests, "autoreg already running", "invalid_request_error", "busy")
		return
	}
	defer autoregMu.Unlock()

	body, ok := readBody(w, r, adminBodyLimit, false)
	if !ok {
		return
	}
	var req struct {
		Provider    string          `json:"provider"`
		Count       int             `json:"count"`
		Proxy       string          `json:"proxy"`
		LabelPrefix string          `json:"label_prefix"`
		Config      json.RawMessage `json:"config"`
		Force       bool            `json:"force"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error", "invalid_body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required", "invalid_request_error", "missing_provider")
		return
	}
	if err := plugin.ValidateName(req.Provider); err != nil {
		writeError(w, http.StatusBadRequest, "invalid provider name: "+err.Error(), "invalid_request_error", "invalid_provider")
		return
	}
	req.LabelPrefix = sanitizeLabel(req.LabelPrefix)
	if req.Proxy != "" {
		if err := plugin.ValidateProxyURL(req.Proxy); err != nil {
			writeError(w, http.StatusBadRequest, "invalid proxy: "+err.Error(), "invalid_request_error", "invalid_proxy")
			return
		}
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 50 {
		writeError(w, http.StatusBadRequest, "count too large (max 50)", "invalid_request_error", "too_many")
		return
	}
	// If config not supplied, try per-plugin isolated file.
	if len(req.Config) == 0 || string(req.Config) == "null" {
		req.Config = plugin.ProviderConfig(s.cfg.PluginsDir, req.Provider)
	} else if len(req.Config) > 1*1024*1024 {
		writeError(w, http.StatusBadRequest, "config too large", "invalid_request_error", "config_too_large")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	created, err := plugin.CreateWithProvider(ctx, req.Provider, req.Config, plugin.CreateOptions{
		Count:       req.Count,
		Proxy:       req.Proxy,
		LabelPrefix: req.LabelPrefix,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "autoreg_failed")
		return
	}
	// Auto-add each created account to the pool (bootstrap).
	type result struct {
		Token   string `json:"token_masked"`
		ID      string `json:"id,omitempty"`
		Email   string `json:"email,omitempty"`
		Error   string `json:"error,omitempty"`
		Label   string `json:"label,omitempty"`
	}
	results := make([]result, 0, len(created))
	added := 0
	for i, acc := range created {
		if acc.TokenV2 == "" {
			results = append(results, result{Error: "empty token_v2", Label: acc.Label})
			continue
		}
		label := acc.Label
		if label == "" && req.LabelPrefix != "" {
			label = req.LabelPrefix
		}
		// sanitize plugin-provided label too
		label = sanitizeLabel(label)
		bctx, bcancel := context.WithTimeout(ctx, 60*time.Second)
		addedAcc, err := s.pool.AddWithBootstrap(bctx, pool.AddInput{
			TokenV2: acc.TokenV2, Label: label, Proxy: acc.Proxy, Force: req.Force,
		})
		bcancel()
		if err != nil {
			results = append(results, result{Token: acc.Label, Error: err.Error(), Label: label})
			_ = i
			continue
		}
		// fire hook
		for _, p := range plugin.List() {
			if hp, ok := p.(plugin.HookProvider); ok {
				hp.OnAccountAdded(addedAcc)
			}
		}
		results = append(results, result{Token: addedAcc.MaskedToken(), ID: addedAcc.ID, Email: addedAcc.Email, Label: addedAcc.Label})
		added++
	}
	writeJSONRaw(w, http.StatusOK, map[string]any{
		"provider": req.Provider,
		"created":  len(created),
		"added":    added,
		"results":  results,
	})
}

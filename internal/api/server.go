// Package api exposes the OpenAI/Anthropic-compatible endpoints, the admin
// API and the shared failover executor that drives the account pool.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/plugin"
	"github.com/shirou-eh/notiongate/internal/pool"
	"github.com/shirou-eh/notiongate/internal/store"
	"github.com/shirou-eh/notiongate/internal/translate"
)

// Server wires configuration, the pool and the HTTP handlers.
type Server struct {
	cfg          *config.Config
	pool         *pool.Pool
	st           *store.Store
	allowedHosts []string
}

// New builds the server.
func New(cfg *config.Config, p *pool.Pool, st *store.Store) *Server {
	return &Server{cfg: cfg, pool: p, st: st, allowedHosts: allowedOriginHosts(cfg)}
}

// Handler builds the HTTP routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.handleOpenAIChat))
	mux.HandleFunc("POST /v1/messages", s.auth(s.handleAnthropicMessages))
	mux.HandleFunc("GET /v1/models", s.auth(s.handleModels))
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("GET /admin/status", s.admin(s.handleAdminStatus))
	mux.HandleFunc("GET /admin/stats", s.admin(s.handleAdminStats))
	mux.HandleFunc("GET /admin/accounts", s.admin(s.handleAdminList))
	mux.HandleFunc("POST /admin/accounts", s.admin(s.handleAdminAdd))
	mux.HandleFunc("PATCH /admin/accounts/{id}", s.admin(s.handleAdminPatch))
	mux.HandleFunc("DELETE /admin/accounts/{id}", s.admin(s.handleAdminDelete))
	mux.HandleFunc("POST /admin/accounts/{id}/test", s.admin(s.handleAdminTest))
	mux.HandleFunc("GET /admin/plugins", s.admin(s.handlePluginsList))
	mux.HandleFunc("POST /admin/autoreg", s.admin(s.handleAutoreg))

	// Let HTTP plugins register their own routes under /plugins/<name>/*
	s.registerPluginRoutes(mux)

	handler := http.Handler(mux)
	// Apply plugin middlewares (outer-most first)
	handler = s.wrapPluginMiddleware(handler)
	return handler
}

// ---- auth ----

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.Header.Get("x-api-key")
}

// secureCompare is a constant-time string comparison for secrets.
func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sameOrigin guards against cross-origin (CSRF-style) browser requests: any
// request that carries an Origin header must target one of the hosts this
// server is actually configured to serve. Comparing against the config (not
// the attacker-controlled Host header) also defeats DNS-rebinding, where a
// hostile domain resolves to 127.0.0.1 and therefore matches r.Host.
// Server-to-server API clients never send Origin, so this costs nothing for
// ADE while blocking browser-based attacks against the no-auth loopback mode.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// Host comparison case-insensitive, with default port handling
	originHost := strings.ToLower(u.Host)
	for _, h := range s.allowedHosts {
		if strings.EqualFold(originHost, strings.ToLower(h)) {
			return true
		}
		// Also allow origin without explicit port when it matches host part
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			if oh, _, err2 := net.SplitHostPort(originHost); err2 == nil {
				if strings.EqualFold(oh, hostOnly) {
					return true
				}
			} else {
				// origin without port
				if strings.EqualFold(originHost, hostOnly) {
					return true
				}
			}
		}
	}
	return false
}

// allowedOriginHosts builds the set of host:port values the server may be
// addressed by (bound host + loopback variants).
func allowedOriginHosts(cfg *config.Config) []string {
	hosts := map[string]bool{}
	for _, h := range []string{cfg.Host, "127.0.0.1", "localhost", "[::1]"} {
		hosts[net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(cfg.Port))] = true
	}
	out := make([]string, 0, len(hosts))
	for h := range hosts {
		out = append(out, h)
	}
	return out
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin requests are not allowed", "authentication_error", "cross_origin")
			return
		}
		if s.cfg.APIKey != "" && !secureCompare(bearer(r), s.cfg.APIKey) {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key", "authentication_error", "invalid_api_key")
			return
		}
		if s.cfg.APIKey == "" && !s.cfg.Loopback() {
			writeError(w, http.StatusServiceUnavailable,
				"notiongate requires API_KEY when bound to a non-loopback interface", "server_error", "auth_required")
			return
		}
		next(w, r)
	}
}

// adminKey: only the dedicated ADMIN_KEY grants remote admin access. Falling
// back to APIKey would hand pool control (and stored proxy credentials) to
// every API client. Loopback stays trusted when no keys are configured.
func (s *Server) adminKey() string {
	return s.cfg.AdminKey
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin requests are not allowed", "authentication_error", "cross_origin")
			return
		}
		key := s.adminKey()
		if key != "" {
			if !secureCompare(bearer(r), key) {
				writeError(w, http.StatusUnauthorized, "invalid or missing admin key", "authentication_error", "invalid_admin_key")
				return
			}
		} else if !s.cfg.Loopback() {
			writeError(w, http.StatusServiceUnavailable,
				"admin API on non-loopback requires ADMIN_KEY (or API_KEY)", "server_error", "auth_required")
			return
		}
		next(w, r)
	}
}

// ---- health ----

// handleHealth stays cheap on purpose: no SQL, no per-account counters, no
// pool details — an unauthenticated endpoint must not become a DoS lever on
// the pool mutex nor fingerprint the pool.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	n, active := s.pool.HealthSummary()
	writeJSONRaw(w, http.StatusOK, map[string]any{
		"ok": true, "accounts": n, "active": active,
	})
}

// ---- shared failover executor ----

type runResult struct {
	AccountID string
	Model     string
	InTok     int64
	OutTok    int64
	Latency   time.Duration
}

func errCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, pool.ErrUnknownModel):
		return "model_not_found"
	case errors.Is(err, notion.ErrModelDisabled):
		return "model_disabled"
	case errors.Is(err, notion.ErrAINotEnabled):
		return "ai_not_enabled"
	case errors.Is(err, notion.ErrAuth):
		return "auth"
	case errors.Is(err, notion.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "upstream"
	}
}

// userNameFromEmail derives a display user name for the context block.
func userNameFromEmail(email, fallback string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	if fallback != "" {
		return fallback
	}
	return "user"
}

// markFailure translates an upstream failure into pool state changes and logs.
// Client-side cancellations are NOT upstream failures: they must not pollute
// LastError or error metrics.
//
// ai_not_enabled: аккаунт СКИПАЕТСЯ — 30m cooldown (не invalid, чтобы мог
// self-heal если AI включат), квота НЕ тратится (RecordUsage ok=false делает
// вызывающий код), запрос transparently failover'ится на следующий аккаунт.
// Клиент видит 403 ai_not_enabled только когда ВСЕ аккаунты перебраны.
func (s *Server) markFailure(id string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	switch {
	case errors.Is(err, notion.ErrAINotEnabled):
		// Скипаем аккаунт: cooldown 30m вместо terminal invalid.
		s.pool.MarkRateLimited(id, 30*time.Minute, "AI not enabled on this space (code: ai_not_enabled): skipped, failover to next account")
	case errors.Is(err, notion.ErrModelDisabled):
		// Ограничение модели планом — аккаунт здоров, cooldown НЕ ставим,
		// только фиксируем причину. Failover всё равно пробует следующий
		// аккаунт (вдруг там другой план), 403 — когда все перебраны.
		s.pool.MarkError(id, "model disabled for plan: "+err.Error())
	case errors.Is(err, notion.ErrAuth):
		s.pool.MarkAuthFailed(id, err.Error())
	case errors.Is(err, notion.ErrRateLimited):
		var rl *notion.RateLimitError
		d := time.Duration(0)
		if errors.As(err, &rl) {
			d = rl.RetryAfter
		}
		s.pool.MarkRateLimited(id, d, err.Error())
	default:
		s.pool.MarkError(id, err.Error())
	}
}

// runInference executes a chat job with account failover.
//
// forward is called for every content event of the successful attempt. For
// streaming handlers it writes SSE chunks (lazily opening the stream on the
// first content event, so failed attempts never emit partial output); for
// non-streaming handlers it just collects events.
//
// Retry semantics: a failure before any content was produced (HTTP status,
// auth, rate limit, ai_not_enabled, upstream 5xx, mid-stream error)
// transparently retries on another account, up to cfg.MaxAttempts distinct
// accounts per request. ai_not_enabled НЕ тратит квоту и НЕ возвращает 403
// сразу — аккаунт скипается (30m cooldown), 403 только если все перебраны.
func (s *Server) runInference(ctx context.Context, job *translate.ChatJob, forward func(notion.Event) error) (runResult, error) {
	mdl, ok := s.pool.LookupModel(job.Model)
	if !ok {
		return runResult{}, pool.ErrUnknownModel
	}
	// Transcript собирается под КАЖДЫЙ аккаунт отдельно (внутри лупа):
	// userId штампуется в user-блоки как у настоящего веб-клиента,
	// а при фейловере аккаунт (и его userId) меняется.
	started := time.Now()

	tried := map[string]bool{}
	var lastErr error

	// ai_not_enabled обязан перебрать ВСЕ живые аккаунты, а не только
	// MaxAttempts (default 3): иначе при 13 аккаунтах запрос падает с 403
	// хотя 4-й аккаунт рабочий. Cap 32 — защита от бесконечного цикла.
	maxTries := s.cfg.MaxAttempts
	if n := len(s.pool.Snapshot()); n > maxTries {
		maxTries = n
	}
	if maxTries > 32 {
		maxTries = 32
	}
	if maxTries < 1 {
		maxTries = 1
	}

	for attempt := 0; attempt < maxTries; attempt++ {
		if ctx.Err() != nil {
			return runResult{}, ctx.Err()
		}
		picked, _, err := s.pool.Pick(job.UserKey, tried)
		if err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		acc := picked.Acc
		tried[acc.ID] = true
		if err := s.pool.AcquirePacing(ctx, acc.ID); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return runResult{}, ctx.Err() // client gone: no doomed upstream call
			}
			// Pacing queue over-subscribed: fail over like an upstream failure.
			lastErr = err
			continue
		}

		cli, err := s.pool.Client(acc.ID)
		if err != nil {
			lastErr = err
			continue
		}
		// userId этого аккаунта — в transcript (как у веб-клиента) и в запрос.
		job.UserID = acc.UserID
		transcript := translate.BuildTranscript(job)
		inTok := translate.TranscriptInputTokens(transcript)
		req := &notion.InferenceRequest{
			SpaceID:     acc.SpaceID,
			UserID:      acc.UserID,
			Email:       acc.Email,
			UserName:    userNameFromEmail(acc.Email, acc.SpaceName),
			SpaceName:   acc.SpaceName,
			SpaceViewID: acc.SpaceViewID,
			Model:       mdl,
			Transcript:  transcript,
		}
		events, cancel, err := cli.RunInferenceStream(ctx, req)
		if err != nil {
			s.markFailure(acc.ID, err)
			s.pool.RecordUsage(acc.ID, job.Protocol, mdl, 0, 0,
				time.Since(started).Milliseconds(), false, errCode(err))
			lastErr = err
			continue
		}

		gotContent := false
		var outTok, realIn, realOut int64
		var streamErr error
		for ev := range events {
			if ev.Kind == notion.EventError {
				streamErr = ev.Err
				break
			}
			if ev.Kind == notion.EventDone {
				break
			}
			if ev.Kind == notion.EventUsage {
				realIn, realOut = ev.TokensIn, ev.TokensOut
				continue
			}
			if !gotContent && (ev.Kind == notion.EventText || ev.Kind == notion.EventReasoning || ev.Kind == notion.EventSearch) {
				// Any upstream signal counts: a tool/search-only reply still
				// proves the stream is alive (prevents pool drainage).
				gotContent = true
				s.pool.MarkUsed(acc.ID)
			}
			switch ev.Kind {
			case notion.EventText, notion.EventReasoning:
				outTok += translate.EstimateTokens(ev.Text)
			}
			if err := forward(ev); err != nil {
				cancel()
				s.pool.RecordUsage(acc.ID, job.Protocol, mdl, inTok, outTok,
					time.Since(started).Milliseconds(), gotContent, "client_aborted")
				return runResult{}, err
			}
		}
		cancel()
		if realOut > 0 {
			outTok = realOut
		}
		if realIn > 0 {
			inTok = realIn
		}

		// An empty stream (no content at all) is an upstream failure —
		// Notion closes instantly when throttled or the space lacks AI.
		if streamErr == nil && !gotContent {
			streamErr = &notion.InferenceError{Err: errors.New("upstream: empty stream (throttled or AI disabled)")}
		}
		if streamErr != nil && strings.Contains(streamErr.Error(), "empty stream") {
			s.pool.MarkThrottled(acc.ID, streamErr.Error())
		}
		if streamErr != nil && errors.Is(streamErr, context.Canceled) {
			// Request context canceled (client gone): log without polluting.
			s.pool.RecordUsage(acc.ID, job.Protocol, mdl, inTok, outTok,
				time.Since(started).Milliseconds(), gotContent, "client")
			lastErr = streamErr
			continue
		}
		if streamErr != nil && !gotContent {
			s.markFailure(acc.ID, streamErr)
			s.pool.RecordUsage(acc.ID, job.Protocol, mdl, inTok, outTok,
				time.Since(started).Milliseconds(), false, errCode(streamErr))
			lastErr = streamErr
			slog.Warn("api: attempt failed, failing over", "attempt", attempt+1, "err", streamErr)
			continue
		}

		if streamErr == nil {
			s.pool.MarkStreamOK(acc.ID)
		}
		res := runResult{
			AccountID: acc.ID,
			Model:     mdl,
			InTok:     inTok,
			OutTok:    outTok,
			Latency:   time.Since(started),
		}
		s.pool.RecordUsage(acc.ID, job.Protocol, mdl, inTok, outTok,
			res.Latency.Milliseconds(), streamErr == nil, errCode(streamErr))
		if streamErr != nil {
			return res, streamErr
		}
		return res, nil
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAccounts
	}
	return runResult{}, lastErr
}

// ---- shared response helpers ----

func writeJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n"))
}

func writeError(w http.ResponseWriter, status int, message, errType, code string) {
	writeJSON(w, status, translate.BuildOpenAIError(message, errType, code))
}

func writeJSONRaw(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "encode")
		return
	}
	writeJSON(w, status, b)
}

// readBody enforces the size limit and renders protocol-appropriate errors
// (OpenAI shape for /v1/*, Anthropic shape for /v1/messages).
func readBody(w http.ResponseWriter, r *http.Request, limit int64, anthropic bool) ([]byte, bool) {
	tooLarge := func() {
		if anthropic {
			writeJSON(w, http.StatusRequestEntityTooLarge,
				translate.BuildAnthropicError("invalid_request_error", "request body too large (limit 4MB)"))
			return
		}
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "body_too_large")
	}
	// Early reject by Content-Length: a large read into RAM per request ×
	// concurrent requests amplifies memory ~8× and can OOM a small VPS.
	if r.ContentLength > limit {
		tooLarge()
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		tooLarge()
		return nil, false
	}
	return body, true
}

func (s *Server) registerPluginRoutes(mux *http.ServeMux) {
	for _, p := range plugin.List() {
		if hp, ok := p.(plugin.HTTPProvider); ok {
			// Recover from plugin panic on duplicate route — don't crash server
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("plugin: RegisterRoutes panic, skipping", "plugin", p.Name(), "panic", r)
					}
				}()
				prefix := "/plugins/" + p.Name()
				// Disallow plugins from hijacking core routes
				if prefix == "/plugins" || p.Name() == "admin" || p.Name() == "v1" || p.Name() == "healthz" {
					slog.Warn("plugin: reserved name, skip routes", "plugin", p.Name())
					return
				}
				hp.RegisterRoutes(mux, s.admin)
				slog.Info("plugin: routes registered", "plugin", p.Name(), "prefix", prefix)
			}()
		}
	}
}

func (s *Server) wrapPluginMiddleware(h http.Handler) http.Handler {
	// Deterministic order: sort by name
	list := plugin.List()
	// sort to make middleware order deterministic (was map iteration random)
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[i].Name() > list[j].Name() {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	for i := len(list) - 1; i >= 0; i-- {
		if hp, ok := list[i].(plugin.HTTPProvider); ok {
			// Recover from middleware panic
			next := h
			pName := list[i].Name()
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Error("plugin middleware panic", "plugin", pName, "panic", rec)
						// fall through to next handler
						next.ServeHTTP(w, r)
					}
				}()
				hp.Middleware(next).ServeHTTP(w, r)
			})
		}
	}
	return h
}

func hasMeaningfulContent(job *translate.ChatJob) bool {
	for _, t := range job.Turns {
		if strings.TrimSpace(t.Text) != "" {
			return true
		}
	}
	for _, s := range job.System {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

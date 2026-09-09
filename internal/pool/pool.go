// Package pool manages the account pool: usage accounting, the 80% rotation
// state machine, failover marks and background health refresh.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/store"
)

// ErrNoAccounts is returned when the pool cannot serve a request.
var ErrNoAccounts = errors.New("pool: no usable account (all exhausted, cooling down or invalid)")

// AccountView is the admin-facing snapshot of one account with live usage.
type AccountView struct {
	model.Account
	EffectiveStatus string  `json:"effective_status"`
	WindowKey       string  `json:"window_key"`
	ReqCount        int64   `json:"req_count"`
	InTokens        int64   `json:"in_tokens"`
	OutTokens       int64   `json:"out_tokens"`
	Limit           int64   `json:"limit"`
	UsageRatio      float64 `json:"usage_ratio"`
}

type state struct {
	acc      model.Account
	builtFor model.Account // account snapshot the client was built from
	cli      *notion.Client
	lastCall time.Time // last upstream inference started (pacing gate)

	consecutiveEmpty int // empty streams in a row → escalating cooldown
	pending          int // pacing waiters currently parked on this account
}

// Pool owns all accounts and their runtime state.
type Pool struct {
	mu     sync.Mutex
	cfg    *config.Config
	store  *store.Store
	accs   map[string]*state
	sticky map[string]string // user key → account id
}

// New creates a pool and loads persisted accounts.
func New(cfg *config.Config, st *store.Store) (*Pool, error) {
	p := &Pool{cfg: cfg, store: st, accs: map[string]*state{}, sticky: map[string]string{}}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Reload re-reads all accounts from the store. The store read happens under
// the pool mutex so a concurrent Update can never be overwritten by a stale
// snapshot.
func (p *Pool) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	accs, err := p.store.ListAccounts()
	if err != nil {
		return fmt.Errorf("pool: load accounts: %w", err)
	}
	keep := map[string]bool{}
	for _, a := range accs {
		keep[a.ID] = true
		if st, ok := p.accs[a.ID]; ok {
			st.acc = a
			// Rebuild the client when connection-affecting fields changed
			// (token/proxy/base are baked into the client).
			b := st.builtFor
			if a.TokenV2 != b.TokenV2 || a.Proxy != b.Proxy || a.BaseURL != b.BaseURL {
				cli, err := notion.NewClient(p.cfg, a)
				if err != nil {
					// Fail closed: exclude from rotation AND persist invalid —
					// a stale (direct-egress) client must never be reused.
					slog.Warn("pool: rebuild client failed, excluding", "id", a.ID, "err", err)
					delete(p.accs, a.ID)
					_ = p.store.UpdateStatus(a.ID, model.StatusInvalid, err.Error())
					continue
				}
				st.cli = cli
				st.builtFor = a
			}
			continue
		}
		cli, err := notion.NewClient(p.cfg, a)
		if err != nil {
			// Fail closed: never fall back to a direct-egress client.
			slog.Warn("pool: client build failed, excluding", "id", a.ID, "err", err)
			_ = p.store.UpdateStatus(a.ID, model.StatusInvalid, err.Error())
			continue
		}
		p.accs[a.ID] = &state{acc: a, builtFor: a, cli: cli}
	}
	for id := range p.accs {
		if !keep[id] {
			delete(p.accs, id)
		}
	}
	for k, id := range p.sticky {
		if !keep[id] {
			delete(p.sticky, k)
		}
	}
	return nil
}

// Add stores a new account (bootstrap already done by caller).
func (p *Pool) Add(a model.Account) error {
	if a.ID == "" {
		a.ID = model.NewID()
	}
	if a.Status == "" {
		a.Status = model.StatusActive
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	// Build the client FIRST: an unparseable proxy must not create a ghost
	// row invisible to admins (R3-6).
	cli, err := notion.NewClient(p.cfg, a)
	if err != nil {
		return err
	}
	if err := p.store.AddAccount(a); err != nil {
		return err
	}
	p.mu.Lock()
	p.accs[a.ID] = &state{acc: a, builtFor: a, cli: cli}
	p.mu.Unlock()
	slog.Info("pool: account added", "id", a.ID, "label", a.Label)
	return nil
}

// Delete removes an account.
func (p *Pool) Delete(id string) error {
	if err := p.store.DeleteAccount(id); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.accs, id)
	for k, v := range p.sticky {
		if v == id {
			delete(p.sticky, k)
		}
	}
	p.mu.Unlock()
	return nil
}

// Get returns a copy of one account.
func (p *Pool) Get(id string) (model.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.accs[id]
	if !ok {
		return model.Account{}, store.ErrNotFound
	}
	return st.acc, nil
}

// Update mutates an account through the given function and persists it.
// The DB write happens under the pool mutex (no write-back races), and the
// upstream client is rebuilt immediately when connection fields changed —
// admin PATCH actions must not wait up to RefreshInterval to take effect.
func (p *Pool) Update(id string, fn func(*model.Account)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.accs[id]
	if !ok {
		return store.ErrNotFound
	}
	fn(&st.acc)
	b := st.builtFor
	if st.acc.TokenV2 != b.TokenV2 || st.acc.Proxy != b.Proxy || st.acc.BaseURL != b.BaseURL {
		cli, err := notion.NewClient(p.cfg, st.acc)
		if err != nil {
			// Fail closed: drop from rotation + persist the reason. The
			// error returned to admin is sanitized (proxy URLs may carry
			// credentials — never echo them).
			slog.Warn("pool: update rebuild failed, excluding", "id", id, "err", err)
			delete(p.accs, id)
			_ = p.store.UpdateStatus(id, model.StatusInvalid, err.Error())
			return errors.New("invalid connection settings (fail-closed): account excluded from rotation")
		}
		st.cli = cli
		st.builtFor = st.acc
	}
	return p.store.UpdateAccount(st.acc)
}

// Client returns the upstream client for an account id.
func (p *Pool) Client(id string) (*notion.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.accs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return st.cli, nil
}

func (p *Pool) effectiveLocked(st *state, now time.Time) string {
	switch st.acc.Status {
	case model.StatusDisabled, model.StatusInvalid:
		return st.acc.Status
	}
	if st.acc.CooldownUntil.After(now) {
		return model.StatusCooldown
	}
	c, err := p.store.GetCounter(st.acc.ID, model.WindowKey(now, st.acc.WindowType))
	if err == nil {
		limit := st.acc.EffectiveLimit(p.cfg.DefaultLimit)
		if limit > 0 {
			ratio := float64(c.ReqCount) / float64(limit)
			if ratio >= 1.0 {
				return model.StatusExhausted
			}
			if ratio >= st.acc.EffectiveRotate(p.cfg.RotateAt) {
				return model.StatusReserve
			}
		}
	}
	return model.StatusActive
}

// Picked is the outcome of a selection: account copy + live client.
type Picked struct {
	Acc    model.Account
	Client *notion.Client
	Status string
}

// Pick selects the best account for a request. Priority:
//  1. sticky account of this user key (if still active),
//  2. active account with the most remaining quota (ties → least recently used),
//  3. reserve accounts (>= rotate threshold but < 100%) as last resort.
//
// Accounts in tried are excluded so a failing request can fail over to
// the next candidate without re-picking the same account.
func (p *Pool) Pick(userKey string, tried map[string]bool) (Picked, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()

	if p.cfg.StickySessions && len(userKey) <= maxStickyKeyBytes {
		if id, ok := p.sticky[userKey]; ok && !tried[id] {
			if st, exists := p.accs[id]; exists && p.effectiveLocked(st, now) == model.StatusActive {
				return Picked{Acc: st.acc, Client: st.cli, Status: model.StatusActive}, model.StatusActive, nil
			}
			delete(p.sticky, userKey)
		}
	}

	type cand struct {
		st        *state
		remaining float64
		status    string
	}
	var active, reserve []cand
	for _, st := range p.accs {
		if tried[st.acc.ID] {
			continue
		}
		status := p.effectiveLocked(st, now)
		if status != model.StatusActive && status != model.StatusReserve {
			continue
		}
		c, _ := p.store.GetCounter(st.acc.ID, model.WindowKey(now, st.acc.WindowType))
		limit := st.acc.EffectiveLimit(p.cfg.DefaultLimit)
		remaining := math.Inf(1)
		if limit > 0 {
			remaining = float64(limit - c.ReqCount)
			if remaining < 0 {
				remaining = 0
			}
		}
		c2 := cand{st: st, remaining: remaining, status: status}
		if status == model.StatusActive {
			active = append(active, c2)
		} else {
			reserve = append(reserve, c2)
		}
	}
	if len(active) == 0 && len(reserve) == 0 {
		return Picked{}, "", ErrNoAccounts
	}
	list := active
	if len(list) == 0 {
		list = reserve
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].remaining != list[j].remaining {
			return list[i].remaining > list[j].remaining
		}
		return list[i].st.acc.LastUsed.Before(list[j].st.acc.LastUsed)
	})
	pick := list[0]
	if p.cfg.StickySessions && len(userKey) <= maxStickyKeyBytes {
		if len(p.sticky) > maxStickyEntries {
			// Targeted eviction: drop a third of the entries (map order is
			// random) instead of nuking every legitimate user's stickiness.
			for k := range p.sticky {
				if len(p.sticky) <= maxStickyEntries*3/4 {
					break
				}
				delete(p.sticky, k)
			}
		}
		p.sticky[userKey] = pick.st.acc.ID
	}
	return Picked{Acc: pick.st.acc, Client: pick.st.cli, Status: pick.status}, pick.status, nil
}

// maxStickyEntries bounds the sticky-session map against key floods;
// maxStickyKeyBytes bounds individual keys (user-controlled strings).
const (
	maxStickyEntries  = 10000
	maxStickyKeyBytes = 256
)

// ErrTooBusy is returned by AcquirePacing when an account's pacing queue is
// over-subscribed: callers must try another account (or back off) instead of
// collapsing into a simultaneous burst that trips Notion's anti-abuse.
var ErrTooBusy = errors.New("pool: account pacing queue is full")

// maxPacingQueue caps how far into the future slots may be reserved per
// account. Beyond this, callers are rejected rather than bursted.
const maxPacingQueue = 16

// AcquirePacing spaces an account's upstream calls at least MinInterval
// apart (with jitter), like a human would. Waiting happens WITHOUT holding a
// reservation: cancelled waiters must not burn future slots (cancel-spam
// would otherwise park the account for maxPacingQueue×interval). Over-long
// projected waits (dense contention) yield ErrTooBusy so the request fails
// over instead of collapsing into a burst.
func (p *Pool) AcquirePacing(ctx context.Context, id string) error {
	p.mu.Lock()
	st := p.accs[id]
	interval := p.cfg.MinInterval
	if interval <= 0 || st == nil {
		if st != nil {
			st.lastCall = time.Now()
		}
		p.mu.Unlock()
		return nil
	}
	// Depth-based backpressure: if too many callers are already parked on
	// this account, reject immediately (503 + Retry-After upstream) instead
	// of piling up goroutines and connections.
	if st.pending >= maxPacingQueue {
		p.mu.Unlock()
		return ErrTooBusy
	}
	st.pending++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		// st may be stale if account was deleted; guard
		if cur, ok := p.accs[id]; ok && cur == st {
			cur.pending--
		} else if st != nil {
			// best-effort decrement on stale pointer
			st.pending--
			if st.pending < 0 {
				st.pending = 0
			}
		}
		p.mu.Unlock()
	}()

	for {
		p.mu.Lock()
		now := time.Now()
		var wait time.Duration
		if st.lastCall.IsZero() || now.Sub(st.lastCall) >= interval {
			st.lastCall = now // slot taken
			p.mu.Unlock()
			return nil
		}
		wait = interval - now.Sub(st.lastCall)
		p.mu.Unlock()

		jitter := time.Duration(time.Now().UnixNano() % int64(interval/2+1))
		select {
		case <-time.After(wait + jitter): // retry: re-contend for the slot
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// MarkThrottled applies an escalating cooldown for soft-throttle signals
// (empty streams): 10min doubling up to 1h, so the proxy never hammers an
// account that Notion has silently throttled.
func (p *Pool) MarkThrottled(id string, reason string) {
	p.mu.Lock()
	var streak int
	if st, ok := p.accs[id]; ok {
		st.consecutiveEmpty++
		streak = st.consecutiveEmpty
	}
	p.mu.Unlock()
	d := time.Duration(10*(1<<uint(min32(streak-1, 6)))) * time.Minute
	if d > time.Hour {
		d = time.Hour
	}
	p.MarkRateLimited(id, d, reason)
}

// MarkStreamOK resets the empty-stream streak on a successful inference.
func (p *Pool) MarkStreamOK(id string) {
	p.mu.Lock()
	if st, ok := p.accs[id]; ok {
		st.consecutiveEmpty = 0
	}
	p.mu.Unlock()
}

func min32(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxLastError caps persisted error strings — a hostile upstream message
// must not flood the DB, admin output or Reload traffic.
const maxLastError = 500

func capErr(s string) string {
	if len(s) > maxLastError {
		return s[:maxLastError] + "…(truncated)"
	}
	return s
}

// MarkAuthFailed flags a dead token (401/403 upstream) — terminal until
// manually re-enabled or re-added.
func (p *Pool) MarkAuthFailed(id, reason string) {
	reason = capErr(reason)
	_ = p.Update(id, func(a *model.Account) {
		a.Status = model.StatusInvalid
		a.LastError = reason
	})
	slog.Warn("pool: account marked invalid", "id", id, "reason", reason)
}

// MarkRateLimited puts an account into cooldown.
func (p *Pool) MarkRateLimited(id string, dur time.Duration, reason string) {
	if dur <= 0 {
		dur = 10 * time.Minute
	}
	_ = p.Update(id, func(a *model.Account) {
		a.Status = model.StatusCooldown
		a.CooldownUntil = time.Now().Add(dur)
		a.LastError = capErr(reason)
	})
	slog.Warn("pool: account cooldown", "id", id, "for", dur, "reason", reason)
}

// MarkError records a transient upstream error without changing status.
func (p *Pool) MarkError(id, reason string) {
	_ = p.Update(id, func(a *model.Account) { a.LastError = capErr(reason) })
}

// MarkUsed touches last-used timestamp and clears transient errors.
func (p *Pool) MarkUsed(id string) {
	_ = p.Update(id, func(a *model.Account) {
		a.LastUsed = time.Now()
		if a.Status == model.StatusCooldown && !a.CooldownUntil.After(time.Now()) {
			a.Status = model.StatusActive
			a.CooldownUntil = time.Time{}
		}
		if a.LastError != "" {
			a.LastError = ""
		}
	})
}

// RecordUsage records one completed request: counters, request log and the
// status recompute that drives 80% rotation.
//
// Deliberate semantics: only requests that produced upstream content (ok=true,
// or client_aborted with content) consume quota counters — a throttled/empty
// stream does not burn Notion-side quota, so counting it would over-rotate.
func (p *Pool) RecordUsage(id, protocol, mdl string, inTok, outTok, latencyMS int64, ok bool, errCode string) {
	now := time.Now()
	if ok {
		p.mu.Lock()
		st, exists := p.accs[id]
		var acc model.Account
		if exists {
			acc = st.acc
		}
		p.mu.Unlock()
		if exists {
			wk := model.WindowKey(now, acc.WindowType)
			if err := p.store.UpsertCounter(id, wk, 1, inTok, outTok); err != nil {
				slog.Error("pool: counter update failed", "err", err)
			}
			p.recomputeStatus(id, acc, now)
		}
	}
	logEntry := model.RequestLog{
		ID: model.NewID(), TS: now, AccountID: id, Model: mdl, Protocol: protocol,
		InTokens: inTok, OutTokens: outTok, LatencyMS: latencyMS,
		Status: map[bool]string{true: "ok", false: "error"}[ok], ErrCode: errCode,
	}
	if err := p.store.InsertRequest(logEntry); err != nil {
		slog.Error("pool: request log failed", "err", err)
	}
}

// recomputeStatus persists status transitions caused by crossing the rotate
// (80%) or exhaustion (100%) thresholds.
func (p *Pool) recomputeStatus(id string, acc model.Account, now time.Time) {
	if acc.Status == model.StatusDisabled || acc.Status == model.StatusInvalid {
		return
	}
	c, err := p.store.GetCounter(id, model.WindowKey(now, acc.WindowType))
	if err != nil {
		return
	}
	limit := acc.EffectiveLimit(p.cfg.DefaultLimit)
	if limit <= 0 {
		return
	}
	ratio := float64(c.ReqCount) / float64(limit)
	next := acc.Status
	switch {
	case ratio >= 1.0:
		next = model.StatusExhausted
	case ratio >= acc.EffectiveRotate(p.cfg.RotateAt):
		next = model.StatusReserve
	case acc.Status == model.StatusReserve || acc.Status == model.StatusExhausted:
		// Window rolled over or usage dropped — return to rotation.
		next = model.StatusActive
	}
	if next != acc.Status {
		_ = p.Update(id, func(a *model.Account) {
			// Fresh check on the live copy: never resurrect an account that
			// was disabled/invalidated between our snapshot and now.
			if a.Status == model.StatusDisabled || a.Status == model.StatusInvalid {
				return
			}
			a.Status = next
		})
		slog.Info("pool: account status", "id", id, "status", next, "ratio", ratio)
	}
}

// HealthSummary is a cheap in-memory count for /healthz (no SQL, no counters).
func (p *Pool) HealthSummary() (total, active int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, st := range p.accs {
		total++
		if st := st; p.effectiveLocked(st, now) == model.StatusActive {
			active++
		}
	}
	return total, active
}

// Snapshot builds admin views for all accounts.
func (p *Pool) Snapshot() []AccountView {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]AccountView, 0, len(p.accs))
	for _, st := range p.accs {
		status := p.effectiveLocked(st, now)
		c, _ := p.store.GetCounter(st.acc.ID, model.WindowKey(now, st.acc.WindowType))
		limit := st.acc.EffectiveLimit(p.cfg.DefaultLimit)
		ratio := 0.0
		if limit > 0 {
			ratio = float64(c.ReqCount) / float64(limit)
		}
		av := st.acc
		av.Proxy = av.ViewProxy() // never expose egress credentials
		v := AccountView{
			Account:         av,
			EffectiveStatus: status,
			WindowKey:       model.WindowKey(now, st.acc.WindowType),
			ReqCount:        c.ReqCount,
			InTokens:        c.InTokens,
			OutTokens:       c.OutTokens,
			Limit:           limit,
			UsageRatio:      ratio,
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// ResolveModel maps a client-facing model name onto a Notion model id.
// Discovery source: models reported by the account, then the built-in
// codename table. Unknown names are passed through (Notion falls back to its
// server-side default).
func (p *Pool) ResolveModel(requested string) string {
	p.mu.Lock()
	// Deterministic build: collect normalized→canonical pairs in account
	// order, then sort by normalized key so first-wins never depends on map
	// iteration order (two accounts may report case variants of one model).
	type pair struct{ n, canon string }
	var pairs []pair
	for _, st := range p.accs {
		for _, m := range st.acc.Models {
			pairs = append(pairs, pair{norm(m), m})
		}
	}
	p.mu.Unlock()
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].n < pairs[j].n })
	known := map[string]string{}
	for _, pr := range pairs {
		if _, exists := known[pr.n]; !exists {
			known[pr.n] = pr.canon
		}
	}
	if len(known) == 0 {
		for name, id := range notion.KnownModels {
			known[norm(name)] = id
			known[norm(id)] = id
		}
	}
	if len(known) == 0 || requested == "" {
		return requested
	}
	if canon, ok := known[norm(requested)]; ok {
		return canon
	}
	req := norm(requested)
	if req == "" {
		return requested // whitespace/control-char request: no fuzzy match
	}
	// Deterministic fuzzy match: collect all candidates, prefer the shortest
	// canonical id, then lexicographic — never rely on map order.
	var candidates []string
	for n, canon := range known {
		if strings.Contains(n, req) || strings.Contains(req, n) {
			candidates = append(candidates, canon)
		}
	}
	if len(candidates) == 0 {
		return requested
	}
	sort.Strings(candidates)
	best := candidates[0]
	for _, c := range candidates {
		if len(c) < len(best) {
			best = c
		}
	}
	return best
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StartRefresher launches the background health checker; blocks until ctx is
// done, so run it in a goroutine.
func (p *Pool) StartRefresher(ctx context.Context) {
	interval := p.cfg.RefreshInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("pool: refresher started", "interval", interval)
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Pick up accounts added/edited by CLI or another process.
			if err := p.Reload(); err != nil {
				slog.Warn("pool: reload failed", "err", err)
			}
			p.refreshOnce(ctx)
		case <-prune.C:
			if err := p.store.PruneRequests(time.Now().AddDate(0, 0, -30)); err != nil {
				slog.Warn("pool: prune failed", "err", err)
			}
		}
	}
}

func (p *Pool) refreshOnce(ctx context.Context) {
	interval2 := p.cfg.RefreshInterval
	if interval2 <= 0 {
		interval2 = 15 * time.Minute
	}
	p.mu.Lock()
	now := time.Now()
	type target struct {
		id  string
		cli *notion.Client
	}
	var targets []target
	for _, st := range p.accs {
		if st.acc.Status == model.StatusDisabled || st.acc.Status == model.StatusInvalid {
			continue
		}
		if now.Sub(st.acc.LastCheck) < interval2 {
			continue
		}
		// Snapshot the client pointer under the mutex: Update/Reload may
		// rebuild it concurrently (live PATCH of token/proxy).
		targets = append(targets, target{id: st.acc.ID, cli: st.cli})
	}
	p.mu.Unlock()

	for _, t := range targets {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := t.cli.Ping(cctx)
		cancel()
		_ = p.Update(t.id, func(a *model.Account) { a.LastCheck = time.Now() })
		switch {
		case errors.Is(err, notion.ErrAuth):
			p.MarkAuthFailed(t.id, "health check: "+err.Error())
		case err != nil:
			p.MarkError(t.id, "health check: "+err.Error())
		default:
			_ = p.Update(t.id, func(a *model.Account) {
				a.LastError = ""
				if a.Status == model.StatusCooldown && !a.CooldownUntil.After(time.Now()) {
					a.Status = model.StatusActive
					a.CooldownUntil = time.Time{}
				}
			})
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// BootstrapToken runs discovery for a raw token_v2 (used by CLI and admin add).
func (p *Pool) BootstrapToken(ctx context.Context, acc model.Account) (*notion.BootstrapInfo, error) {
	cli, err := notion.NewClient(p.cfg, acc)
	if err != nil {
		return nil, err
	}
	return cli.Bootstrap(ctx)
}

// TestAccount pings a stored account and updates its status accordingly.
func (p *Pool) TestAccount(ctx context.Context, id string) error {
	cli, err := p.Client(id)
	if err != nil {
		return err
	}
	err = cli.Ping(ctx)
	switch {
	case err == nil:
		_ = p.Update(id, func(a *model.Account) {
			a.LastCheck = time.Now()
			a.LastError = ""
			if a.Status == model.StatusInvalid {
				a.Status = model.StatusActive
			}
		})
	case errors.Is(err, notion.ErrAuth):
		p.MarkAuthFailed(id, "manual test: "+err.Error())
	default:
		p.MarkError(id, "manual test: "+err.Error())
	}
	return err
}

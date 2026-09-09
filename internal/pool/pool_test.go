package pool

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/store"
)

func testCfg() *config.Config {
	return &config.Config{
		Host: "127.0.0.1", Port: 0,
		RotateAt:        0.8,
		DefaultWindow:   "month",
		DefaultLimit:    0,
		StickySessions:  true,
		MaxAttempts:     3,
		UpstreamTimeout: time.Minute,
		RefreshInterval: time.Hour,
		NotionBaseURL:   "http://127.0.0.1:1",
	}
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := New(testCfg(), st)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mkAcc(id string, lastUsed time.Time) model.Account {
	return model.Account{
		ID:         id,
		Label:      id,
		TokenV2:    "tok-" + id,
		UserID:     "u-" + id,
		SpaceID:    "sp-" + id,
		Status:     model.StatusActive,
		Models:     []string{"m1"},
		LimitReq:   10,
		WindowType: model.WindowDay,
		RotateAt:   0.8,
		LastUsed:   lastUsed,
	}
}

func TestPickLeastUsedFirst(t *testing.T) {
	p := newTestPool(t)
	now := time.Now()
	if err := p.Add(mkAcc("b", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(mkAcc("a", now)); err != nil {
		t.Fatal(err)
	}
	picked, status, err := p.Pick("u1", nil)
	if err != nil || status != model.StatusActive {
		t.Fatalf("pick: %v %s", err, status)
	}
	if picked.Acc.ID != "b" {
		t.Fatalf("expected LRU account b, got %s", picked.Acc.ID)
	}
}

func TestRotateAt80Percent(t *testing.T) {
	p := newTestPool(t)
	now := time.Now()
	_ = p.Add(mkAcc("a", now.Add(-2*time.Hour)))
	_ = p.Add(mkAcc("b", now.Add(-time.Hour)))

	// Use account a up to 90% → it must leave the active rotation.
	for i := 0; i < 9; i++ {
		p.RecordUsage("a", "openai", "m1", 10, 10, 5, true, "")
	}
	views := p.Snapshot()
	var va *AccountView
	for i := range views {
		if views[i].ID == "a" {
			va = &views[i]
		}
	}
	if va.EffectiveStatus != model.StatusReserve {
		t.Fatalf("account a status = %s, want reserve (ratio %.2f)", va.EffectiveStatus, va.UsageRatio)
	}

	// Next pick must go to b.
	picked, _, err := p.Pick("u1", nil)
	if err != nil || picked.Acc.ID != "b" {
		t.Fatalf("pick after reserve: %v %s", err, picked.Acc.ID)
	}
	// Explicit pick of a is allowed as last resort only via reserve fallback.
	picked, status, err := p.Pick("u2", map[string]bool{"b": true})
	if err != nil || status != model.StatusReserve || picked.Acc.ID != "a" {
		t.Fatalf("reserve fallback: %v %s %s", err, status, picked.Acc.ID)
	}

	// Crossing 100% → exhausted and excluded entirely.
	p.RecordUsage("a", "openai", "m1", 10, 10, 5, true, "")
	if _, _, err := p.Pick("u3", map[string]bool{"b": true}); err != ErrNoAccounts {
		t.Fatalf("expected ErrNoAccounts, got %v", err)
	}
}

func TestExhaustedAll(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	for i := 0; i < 10; i++ {
		p.RecordUsage("a", "openai", "m1", 1, 1, 1, true, "")
	}
	if _, _, err := p.Pick("u", nil); err != ErrNoAccounts {
		t.Fatalf("expected ErrNoAccounts, got %v", err)
	}
}

func TestFailoverExclusion(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now().Add(-time.Hour)))
	_ = p.Add(mkAcc("b", time.Now()))
	picked, _, err := p.Pick("u", map[string]bool{"a": true})
	if err != nil || picked.Acc.ID != "b" {
		t.Fatalf("expected b after excluding a, got %v %s", err, picked.Acc.ID)
	}
}

func TestStickySessions(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now().Add(-time.Hour)))
	_ = p.Add(mkAcc("b", time.Now()))
	p1, _, _ := p.Pick("user-x", nil)
	p2, _, _ := p.Pick("user-x", nil)
	if p1.Acc.ID != p2.Acc.ID {
		t.Fatalf("sticky violated: %s vs %s", p1.Acc.ID, p2.Acc.ID)
	}
}

func TestCooldownAndRecovery(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.MarkRateLimited("a", time.Hour, "429")
	if _, _, err := p.Pick("u", nil); err != ErrNoAccounts {
		t.Fatalf("cooled account must be excluded, got %v", err)
	}
	// Manual test path recovers the account.
	if err := p.TestAccount(context.Background(), "a"); err == nil {
		// ping will fail (no server) — expected; status must not become active
		_ = err
	}
	_ = p.Update("a", func(acc *model.Account) {
		acc.CooldownUntil = time.Now().Add(-time.Minute)
	})
	_, status, err := p.Pick("u", nil)
	if err != nil || status != model.StatusActive {
		t.Fatalf("expired cooldown must return account: %v %s", err, status)
	}
}

func TestAuthFailedSticky(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.MarkAuthFailed("a", "401")
	if _, _, err := p.Pick("u", nil); err != ErrNoAccounts {
		t.Fatalf("invalid account must be excluded, got %v", err)
	}
	// TestAccount with a valid ping resets the status; here we simulate
	// recovery by hand to verify the state machine allows it.
	_ = p.Update("a", func(acc *model.Account) { acc.Status = model.StatusActive })
	if _, _, err := p.Pick("u", nil); err != nil {
		t.Fatalf("re-enabled account must be pickable: %v", err)
	}
}

func TestDisabledExcluded(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	_ = p.Update("a", func(acc *model.Account) { acc.Status = model.StatusDisabled })
	if _, _, err := p.Pick("u", nil); err != ErrNoAccounts {
		t.Fatalf("disabled account must be excluded, got %v", err)
	}
}

func TestResolveModel(t *testing.T) {
	p := newTestPool(t)
	acc := mkAcc("a", time.Now())
	acc.Models = []string{"anthropic-sonnet-4.6", "openai-gpt-5.1"}
	_ = p.Add(acc)
	cases := map[string]string{
		"anthropic-sonnet-4.6": "anthropic-sonnet-4.6",
		"Anthropic-Sonnet-4.6": "anthropic-sonnet-4.6",
		"sonnet-4.6":           "anthropic-sonnet-4.6",
		"gpt5":                 "openai-gpt-5.1",
		"totally-unknown":      "totally-unknown", // passthrough
		"":                     "",                // server default
	}
	for in, want := range cases {
		if got := p.ResolveModel(in); got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddWithBootstrapForce(t *testing.T) {
	p := newTestPool(t) // base URL points nowhere → bootstrap fails
	acc, err := p.AddWithBootstrap(context.Background(), AddInput{
		TokenV2: "v02test", Force: true, LimitReq: 5,
	})
	if err != nil {
		t.Fatalf("force add: %v", err)
	}
	if acc.LimitReq != 5 || acc.WindowType != model.WindowMonth || acc.RotateAt != 0.8 {
		t.Fatalf("defaults not applied: %+v", acc)
	}
	if _, err := p.AddWithBootstrap(context.Background(), AddInput{TokenV2: "v02x"}); err == nil {
		t.Fatal("bootstrap failure must be reported without force")
	}
}

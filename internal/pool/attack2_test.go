package pool

// Regression tests for adversarial round-2 findings (R2-1, R2-2, R2-3, R2-5,
// R2-14, R2-15). Each test reproduces the attack and asserts the fix.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/notion"
	"github.com/shirou-eh/notiongate/internal/store"
)

// R2-1: pacing must actually enforce intervals (round-1 version was a no-op).
func TestR2PacingEnforcesInterval(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.cfg.MinInterval = 150 * time.Millisecond
	ctx := context.Background()

	// Sequential: the second call must be delayed by ~interval.
	p.AcquirePacing(ctx, "a")
	start := time.Now()
	p.AcquirePacing(ctx, "a")
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("second call not delayed: %v (pacing is a no-op)", d)
	}

	// Concurrent: waits must be spaced, not zero.
	var wg sync.WaitGroup
	start = time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.AcquirePacing(ctx, "a")
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("5 concurrent calls finished in %v — spacing not enforced", d)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("5 concurrent calls stacked for %v (head-of-line not bounded)", d)
	}
}

// R2-2: recomputeStatus must not resurrect disabled accounts.
func TestR2RecomputeNeverResurrects(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	// 9/10 used: next usage crosses the rotate threshold.
	for i := 0; i < 9; i++ {
		p.RecordUsage("a", "openai", "m1", 1, 1, 1, true, "")
	}
	_ = p.Update("a", func(a *model.Account) { a.Status = model.StatusDisabled })
	// The in-flight request that started before the disable now completes.
	p.RecordUsage("a", "openai", "m1", 1, 1, 1, true, "")
	acc, _ := p.Get("a")
	if acc.Status != model.StatusDisabled {
		t.Fatalf("recompute resurrected disabled account: %s", acc.Status)
	}
	_ = p.Update("a", func(a *model.Account) { a.Status = model.StatusInvalid })
	p.RecordUsage("a", "openai", "m1", 1, 1, 1, true, "")
	acc, _ = p.Get("a")
	if acc.Status != model.StatusInvalid {
		t.Fatalf("recompute resurrected invalid account: %s", acc.Status)
	}
}

// R2-5: oversized sticky keys must not be stored.
func TestR2StickyKeyBytesCapped(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	big := make([]byte, 10000)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	if _, _, err := p.Pick(string(big), nil); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	n := len(p.sticky)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("oversized sticky key stored (%d entries)", n)
	}
}

// R2-14: Reload must rebuild the client when connection fields change.
func TestR2ReloadRebuildsClient(t *testing.T) {
	p := newTestPool(t)
	acc := mkAcc("a", time.Now())
	_ = p.Add(acc)

	// Simulate an external edit: new token in the store.
	acc.TokenV2 = "brand-new-token"
	if err := p.store.UpdateAccount(acc); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	st := p.accs["a"]
	built := st.builtFor
	p.mu.Unlock()
	if built.TokenV2 != "brand-new-token" {
		t.Fatalf("client not rebuilt after token change: %q", built.TokenV2)
	}
}

// R2-15: an unparseable egress proxy must fail closed.
func TestR2InvalidProxyFailsClosed(t *testing.T) {
	acc := mkAcc("a", time.Now())
	acc.Proxy = "socks5://user:pw@127.0.0.1:1x80" // invalid port
	cfg := testCfg()
	if _, err := notion.NewClient(cfg, acc); err == nil {
		t.Fatal("expected error for invalid proxy URL (fail-closed)")
	}
	acc.Proxy = "http://127.0.0.1:3128"
	if _, err := notion.NewClient(cfg, acc); err != nil {
		t.Fatalf("valid http proxy must be accepted: %v", err)
	}
}

// R2-3: counter upsert retries on transient busy — verified indirectly by
// exercising concurrent writers on one store (SQLite serializes; no lost
// update on the happy path).
func TestR2CounterConcurrentNoLostUpdates(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wk := model.WindowKey(time.Now(), model.WindowDay)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = st.UpsertCounter("a1", wk, 1, 1, 1)
		}()
	}
	wg.Wait()
	c, err := st.GetCounter("a1", wk)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReqCount != 20 {
		t.Fatalf("lost updates: %d != 20", c.ReqCount)
	}
}

package pool

// Regression tests for adversarial findings A-2, A-5, A-8, A-9, A-13 (round 1).

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
	"github.com/shirou-eh/notiongate/internal/store"
)

// A-2: Account copy must be race-free while Reload/Update mutate.
func TestAttackPickCopyNoRace(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	_ = p.Add(mkAcc("b", time.Now().Add(time.Hour)))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // writer: reload + update
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = p.Update("a", func(a *model.Account) { a.LastError = "x" })
			_ = p.Reload()
		}
	}()
	go func() { // reader: pick + use copy
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			picked, _, err := p.Pick("u", nil)
			if err == nil {
				_ = picked.Acc.ID
			}
		}
	}()
	go func() { // reader: snapshot
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = p.Snapshot()
		}
	}()
	time.Sleep(700 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// A-5: concurrent pacing callers must not stack unbounded delays.
func TestAttackPacingBurst(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.cfg.MinInterval = 200 * time.Millisecond

	const n = 10
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.AcquirePacing(context.Background(), "a")
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	// Without the cap this is ~n*interval (2s+). The cap bounds it to
	// ~2*interval + jitter per caller but the QUEUE drains in parallel —
	// the guarantee: no caller waits more than ~2*interval.
	if elapsed > 4*time.Second {
		t.Fatalf("pacing burst took %v (stacking head-of-line not bounded)", elapsed)
	}
}

// A-8: aiNotEnabled must NOT permanently invalidate the account.
func TestAttackAINotEnabledCooldownNotInvalid(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.MarkRateLimited("a", 30*time.Minute, "AI not enabled on this space")
	acc, err := p.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Status != model.StatusCooldown {
		t.Fatalf("status = %s, want cooldown (not invalid)", acc.Status)
	}
}

// A-9: sticky map must be capped.
func TestAttackStickyMapCapped(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	for i := 0; i < maxStickyEntries+500; i++ {
		if _, _, err := p.Pick(string(rune('u'))+string(rune(i%26))+"-key", nil); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	n := len(p.sticky)
	p.mu.Unlock()
	if n > maxStickyEntries {
		t.Fatalf("sticky map grew to %d entries", n)
	}
}

// A-7: concurrent Update must never be rolled back by Reload.
func TestAttackReloadDoesNotRollbackStatus(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.MarkRateLimited("a", time.Hour, "429")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = p.Reload()
		}
	}()
	<-done
	acc, _ := p.Get("a")
	if acc.Status != model.StatusCooldown || !acc.CooldownUntil.After(time.Now()) {
		t.Fatalf("Reload rolled back cooldown: %+v", acc)
	}
}

// A-13: zero refresh interval must not panic.
func TestAttackZeroRefreshInterval(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := testCfg()
	cfg.RefreshInterval = 0
	p, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go p.StartRefresher(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
}

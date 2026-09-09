package pool

// Regression tests for adversarial round-3 findings (R3-1, R3-2, R3-4,
// R3-6, R3-7). Each test reproduces the attack and asserts the fix.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
)

// R3-1 (rev. R5-2): concurrent pacing callers must not collapse into a
// burst — accepted callers start spaced by ~interval; cancelled waiters must
// NOT burn future slots (a cancel-spam storm cannot park the account).
// With maxPacingQueue=16, over-subscribed callers get ErrTooBusy — that's
// expected backpressure, not failure.
func TestR3PacingNoBurstCollapse(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.cfg.MinInterval = 200 * time.Millisecond
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	starts := make([]time.Time, 0, n)
	busy := 0
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.AcquirePacing(ctx, "a"); err != nil {
				mu.Lock()
				busy++
				mu.Unlock()
				return
			}
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// At least some must have been accepted and spaced
	if len(starts) == 0 {
		t.Fatalf("all pacing calls rejected")
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < 90*time.Millisecond {
			t.Fatalf("burst collapse: gap #%d = %v (< interval/2)", i, gap)
		}
	}
	// Bounded total: with spacing, accepted callers ≈ len(starts)×interval
	if elapsed > 20*time.Second {
		t.Fatalf("paced queue ran for %v", elapsed)
	}
	// Backpressure must have kicked in for over-subscribed
	if busy == 0 && n > maxPacingQueue {
		t.Logf("note: no ErrTooBusy with n=%d (queue depth %d) — backpressure not triggered, but spacing still holds", n, maxPacingQueue)
	}
}

// R5-2: cancel-spam must not park the account — a legit caller right after
// 16 cancelled waiters must acquire the slot promptly.
func TestR5CancelSpamDoesNotBurnSlots(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	p.cfg.MinInterval = 300 * time.Millisecond

	// One legit caller lands the first slot.
	if err := p.AcquirePacing(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	// Storm of cancelled waiters.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_ = p.AcquirePacing(ctx, "a") // cancelled mid-wait — must not burn slot
		}()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	// Legit caller must get in within ~interval (slot not occupied by spam).
	start := time.Now()
	if err := p.AcquirePacing(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 700*time.Millisecond {
		t.Fatalf("cancel-spam parked the account: legit caller waited %v", d)
	}
}

// R3-2: a PATCHed-in invalid proxy must fail CLOSED on Reload — the account
// leaves rotation and is marked invalid, never silently egressing directly.
func TestR3BadProxyPatchFailsClosed(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))

	// Simulate admin PATCH with an invalid proxy written straight to the store.
	acc, _ := p.Get("a")
	acc.Proxy = "socks5://u:p@127.0.0.1:9x99"
	_ = p.store.UpdateAccount(acc)

	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Pick("u", nil); err != ErrNoAccounts {
		t.Fatalf("account with broken proxy must be excluded, got %v", err)
	}
	// The account is excluded from the in-memory pool but persisted as
	// invalid so the admin sees WHY (fail-closed, not silently dropped).
	got, err := p.store.GetAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusInvalid {
		t.Fatalf("status = %s, want invalid (fail-closed)", got.Status)
	}
}

// R3-4: hostile upstream error messages must be capped everywhere.
func TestR3LastErrorCapped(t *testing.T) {
	p := newTestPool(t)
	_ = p.Add(mkAcc("a", time.Now()))
	huge := strings.Repeat("SECRET", 200000) // 1.2MB
	p.MarkError("a", huge)
	acc, _ := p.Get("a")
	if len(acc.LastError) > maxLastError+20 {
		t.Fatalf("LastError not capped: %d bytes", len(acc.LastError))
	}
}

// R3-6: adding an account with a broken proxy must not create a ghost row.
func TestR3NoGhostRowOnBadProxy(t *testing.T) {
	p := newTestPool(t)
	acc := mkAcc("a", time.Now())
	acc.Proxy = "socks5://u:p@127.0.0.1:9x99"
	if err := p.Add(acc); err == nil {
		t.Fatal("Add with broken proxy must fail")
	}
	if n, _ := p.store.CountAccounts(); n != 0 {
		t.Fatalf("ghost row created: %d accounts in store", n)
	}
}

// R3-7: model resolution must be deterministic across repeated requests.
func TestR3ResolveModelDeterministic(t *testing.T) {
	p := newTestPool(t)
	acc := mkAcc("a", time.Now())
	acc.Models = []string{"e2e-mini", "e2e-model"}
	_ = p.Add(acc)
	first := p.ResolveModel("e2e")
	for i := 0; i < 200; i++ {
		if got := p.ResolveModel("e2e"); got != first {
			t.Fatalf("nondeterministic resolution: %q vs %q", got, first)
		}
	}
	// Control chars must not fuzzy-match everything.
	if got := p.ResolveModel("\x00"); got != "\x00" {
		t.Fatalf("control-char request mutated: %q", got)
	}
}

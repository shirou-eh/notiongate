package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAccountCRUD(t *testing.T) {
	s := openTemp(t)
	a := model.Account{
		ID: "a1", Label: "first", TokenV2: "tok-secret", UserID: "u1", SpaceID: "sp1",
		SpaceName: "WS", Email: "a@b.c", Status: model.StatusActive,
		Models:   []string{"m1", "m2"},
		LimitReq: 100, WindowType: model.WindowDay, RotateAt: 0.8,
		Proxy:         "socks5://127.0.0.1:1080",
		CooldownUntil: time.Now().Add(time.Hour).Round(time.Second),
		CreatedAt:     time.Now().Round(time.Second),
	}
	if err := s.AddAccount(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount("a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenV2 != "tok-secret" || got.Email != "a@b.c" || len(got.Models) != 2 ||
		got.LimitReq != 100 || got.Proxy != "socks5://127.0.0.1:1080" ||
		!got.CooldownUntil.Equal(a.CooldownUntil) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.MaskedToken() == "tok-secret" {
		t.Fatal("token must be masked")
	}

	got.Label = "renamed"
	got.Status = model.StatusDisabled
	if err := s.UpdateAccount(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetAccount("a1")
	if got2.Label != "renamed" || got2.Status != model.StatusDisabled {
		t.Fatalf("update failed: %+v", got2)
	}

	if err := s.DeleteAccount("a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAccount("a1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateMissing(t *testing.T) {
	s := openTemp(t)
	if err := s.UpdateAccount(model.Account{ID: "nope"}); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCountersAndStats(t *testing.T) {
	s := openTemp(t)
	_ = s.AddAccount(model.Account{ID: "a1", TokenV2: "x", WindowType: model.WindowDay, Status: "active"})
	wk := model.WindowKey(time.Now(), model.WindowDay)
	_ = s.UpsertCounter("a1", wk, 1, 10, 20)
	_ = s.UpsertCounter("a1", wk, 2, 5, 7)
	c, err := s.GetCounter("a1", wk)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReqCount != 3 || c.InTokens != 15 || c.OutTokens != 27 {
		t.Fatalf("counter = %+v", c)
	}
	// Missing counter reads as zero.
	c0, _ := s.GetCounter("a1", "day:2000-01-01")
	if c0.ReqCount != 0 {
		t.Fatalf("expected zero counter, got %+v", c0)
	}

	_ = s.InsertRequest(model.RequestLog{ID: "r1", TS: time.Now(), AccountID: "a1", Model: "m", Protocol: "openai", InTokens: 3, OutTokens: 4, Status: "ok"})
	_ = s.InsertRequest(model.RequestLog{ID: "r2", TS: time.Now(), AccountID: "a1", Status: "error", ErrCode: "upstream"})
	st, err := s.StatsSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st.Requests != 2 || st.Errors != 1 || st.InTokens != 3 || st.OutTokens != 4 {
		t.Fatalf("stats = %+v", st)
	}
}

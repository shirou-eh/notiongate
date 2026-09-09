package notion

// Regression tests for adversarial findings A-1, A-3, A-6 (round 1).
// Each test reproduces the original attack and asserts the fix.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/config"
	"github.com/shirou-eh/notiongate/internal/model"
)

func testCfg(base string, timeout time.Duration) *config.Config {
	return &config.Config{NotionBaseURL: base, UpstreamTimeout: timeout}
}

// A-1: malformed patch line without patch-start must not panic the process.
func TestAttackPatchWithoutPatchStartNoPanic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"agent-inference","value":[{"type":"text","content":"x"}]}}]}` + "\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli, err := NewClient(testCfg(srv.URL, time.Minute), model.Account{TokenV2: "x", UserID: "u", SpaceID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := cli.RunInferenceStream(context.Background(), &InferenceRequest{SpaceID: "s", UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	got := false
	for range events {
		got = true // any events (incl. recovered error) prove no crash
	}
	if !got {
		t.Fatal("stream produced no events")
	}
}

// A-1b: pure parser fuzz — junk must be tolerated without panics.
func TestParserJunkTolerance(t *testing.T) {
	junk := []string{
		`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"agent-inference","value":[{"type":"text","content":"x"}]}}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"error"}}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/999999999/value/-","v":{"type":"text","content":"big"}}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/0/value/-","v":"just a string"}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/-/value/","v":{"type":"text","content":"weird path"}}]}`,
		`{"type":"patch","v":[{"o":"x","p":"/s/0/value/0/content","v":123}]}`,
		`{"type":"patch","v":"not-an-array"}`,
		`{"type":"patch-start","data":null}`,
		`{"type":"patch-start"}`,
		`not json at all`,
		`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"agent-inference","value":"not-a-list"}}]}`,
		`{"type":"patch","v":[{"o":"a","p":"/s/-","v":{"type":"agent-inference","value":[{"type":"text"}]}}]}`,
	}
	for _, line := range junk {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parser panicked on %q: %v", line, r)
				}
			}()
			_ = runParser(t, line+"\n")
		}()
	}
}

// A-3: a silent upstream must not hang the stream forever.
func TestAttackSilentUpstreamTimesOut(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send anything
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli, err := NewClient(testCfg(srv.URL, 300*time.Millisecond), model.Account{TokenV2: "x"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	events, cancel, err := cli.RunInferenceStream(context.Background(), &InferenceRequest{SpaceID: "s", UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	for range events {
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stream hung for %v, want timeout ~300ms", elapsed)
	}
}

// A-3b: cancel() must abort body reads promptly.
func TestAttackCancelAbortsRead(t *testing.T) {
	block := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-block
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli, err := NewClient(testCfg(srv.URL, 0), model.Account{TokenV2: "x"})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := cli.RunInferenceStream(context.Background(), &InferenceRequest{SpaceID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(block)
		t.Fatal("cancel() did not abort the stream read")
	}
	close(block)
}

// A-6: absurd Retry-After must be clamped.
func TestAttackRetryAfterClamped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "9223372036") // ~292 years
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli, err := NewClient(testCfg(srv.URL, time.Minute), model.Account{TokenV2: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cli.RunInferenceStream(context.Background(), &InferenceRequest{SpaceID: "s"})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	rl, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	if rl.RetryAfter > 10*time.Minute {
		t.Fatalf("RetryAfter not clamped: %v", rl.RetryAfter)
	}
}

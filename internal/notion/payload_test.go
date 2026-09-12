package notion

// Payload parity with the real Notion web client (verified against
// notion-forge HAR captures + two independent transcript.py mirrors):
//   - top-level patchResponseVersion: 2
//   - transcript[2] is user-specified-context with empty pointers
//   - every user block carries userId
//   - config carries modelFromUser:true + the requested codename

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shirou-eh/notiongate/internal/model"
)

func TestPayloadMatchesRealClient(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/runInferenceTranscript", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("{\"type\":\"patch-start\",\"data\":{\"s\":[]}}\n"))
		_, _ = w.Write([]byte("{\"type\":\"patch\",\"v\":[{\"o\":\"a\",\"p\":\"/s/-\",\"v\":{\"id\":\"m1\",\"type\":\"agent-inference\",\"value\":[{\"type\":\"text\",\"content\":\"ok\"}]}}]}\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli, err := NewClient(testCfg(srv.URL, time.Minute), model.Account{TokenV2: "x", UserID: "u-1", SpaceID: "s-1"})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := cli.RunInferenceStream(context.Background(), &InferenceRequest{
		SpaceID: "s-1", UserID: "u-1", Email: "u@test.io", UserName: "u",
		Model:      "angel-cake-high",
		Transcript: []TranscriptEntry{UserBlock("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	for range events {
	}
	if len(gotBody) == 0 {
		t.Fatal("no request captured")
	}
	var payload struct {
		PatchResponseVersion int `json:"patchResponseVersion"`
		Transcript           []struct {
			Type   string          `json:"type"`
			Value  json.RawMessage `json:"value"`
			UserID string          `json:"userId"`
		} `json:"transcript"`
		ConfigProbe struct {
		} `json:"-"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload.PatchResponseVersion != 2 {
		t.Fatalf("patchResponseVersion = %d, want 2", payload.PatchResponseVersion)
	}
	if len(payload.Transcript) < 4 {
		t.Fatalf("transcript blocks = %d, want >= 4 (config/context/user-specified-context/user)", len(payload.Transcript))
	}
	if payload.Transcript[0].Type != "config" || payload.Transcript[1].Type != "context" {
		t.Fatalf("blocks[0:2] = %q/%q, want config/context",
			payload.Transcript[0].Type, payload.Transcript[1].Type)
	}
	if payload.Transcript[2].Type != "user-specified-context" {
		t.Fatalf("blocks[2] = %q, want user-specified-context", payload.Transcript[2].Type)
	}
	var usc struct {
		Pointers []any `json:"pointers"`
	}
	if err := json.Unmarshal(payload.Transcript[2].Value, &usc); err != nil || usc.Pointers == nil {
		t.Fatalf("user-specified-context must carry pointers array: %s", payload.Transcript[2].Value)
	}
	// config: modelFromUser + codename
	var cfg struct {
		Model         string `json:"model"`
		ModelFromUser bool   `json:"modelFromUser"`
	}
	if err := json.Unmarshal(payload.Transcript[0].Value, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ModelFromUser || cfg.Model != "angel-cake-high" {
		t.Fatalf("config = %+v, want modelFromUser=true model=angel-cake-high", cfg)
	}
	// все user-блоки с userId
	foundUser := false
	for _, b := range payload.Transcript {
		if b.Type != "user" {
			continue
		}
		foundUser = true
		if b.UserID != "u-1" {
			t.Fatalf("user block without userId: %+v", b)
		}
	}
	if !foundUser {
		t.Fatal("no user blocks in transcript")
	}
}

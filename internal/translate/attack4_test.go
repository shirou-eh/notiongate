package translate

// Regression tests for adversarial round-4 findings (R4-1).

import (
	"strings"
	"testing"
	"time"
)

// R4-1: the merge must be O(n) — thousands of consecutive same-role messages
// must not burn seconds of CPU (the old code was quadratic re-copying).
func TestR4MergeLinearScaling(t *testing.T) {
	const n = 8000
	msgs := make([]Turn, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, Turn{Role: "user", Text: "x"})
	}
	start := time.Now()
	tr := BuildTranscript(&ChatJob{Turns: msgs})
	elapsed := time.Since(start)
	if len(tr) != 1 {
		t.Fatalf("expected 1 merged entry, got %d", len(tr))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("merge is quadratic: %d messages took %v", n, elapsed)
	}
	arr, ok := tr[0].Value.([][]string)
	if !ok || len(arr) == 0 {
		t.Fatal("bad merged value shape")
	}
	if got := arr[0][0]; strings.Count(got, "x") != n {
		t.Fatalf("merged text lost content: %d x's", strings.Count(got, "x"))
	}
}

// R4-1b: empty/whitespace turns vanish; roles alternate correctly.
func TestR4MergeRoleRuns(t *testing.T) {
	tr := BuildTranscript(&ChatJob{Turns: []Turn{
		{Role: "user", Text: "a"},
		{Role: "user", Text: "   "},
		{Role: "user", Text: "b"},
		{Role: "assistant", Text: "c"},
		{Role: "assistant", Text: "d"},
		{Role: "user", Text: "e"},
	}})
	if len(tr) != 3 {
		t.Fatalf("transcript = %d entries, want 3", len(tr))
	}
	if tr[0].Type != "user" || tr[1].Type != "agent-inference" || tr[2].Type != "user" {
		t.Fatalf("wrong type sequence: %v", tr)
	}
}

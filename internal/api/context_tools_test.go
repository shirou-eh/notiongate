package api

import (
	"context"
	"strings"
	"testing"

	"github.com/shirou-eh/notiongate/internal/translate"
)

func TestPrepareJobConfigPlacementNoToolText(t *testing.T) {
	job := &translate.ChatJob{
		Model: "sonnet-5", Protocol: "openai",
		Turns:          []translate.Turn{{Role: "user", Text: "Создай файл /tmp/x.txt."}},
		Tools:          []translate.Tool{{Name: "Edit", Description: "edit"}},
		ToolChoice:     "auto",
		ToolsPlacement: "config",
	}
	prepareJob(context.Background(), job)
	if job.ToolsSpecJSON == "" || !strings.Contains(job.ToolsSpecJSON, `"name":"Edit"`) {
		t.Fatalf("spec missing: %q", job.ToolsSpecJSON)
	}
	for _, s := range job.System {
		if strings.Contains(s, "tool_calls") || strings.Contains(s, "Available tools") {
			t.Fatalf("config placement must not leak tool text into system: %q", s)
		}
	}
}

func TestPrepareJobDemoPrepended(t *testing.T) {
	job := &translate.ChatJob{
		Model: "sonnet-5", Protocol: "openai",
		Turns:      []translate.Turn{{Role: "user", Text: "real task"}},
		Tools:      []translate.Tool{{Name: "Edit"}},
		ToolChoice: "auto",
		ToolsDemo:  true,
	}
	prepareJob(context.Background(), job)
	if len(job.Turns) < 5 || job.Turns[0].Text != "Create file /tmp/notiongate-demo.txt with text demo." {
		t.Fatalf("demo not prepended: %+v", job.Turns)
	}
	if !strings.Contains(job.Turns[1].Text, "call_demo") {
		t.Fatalf("demo call marker missing: %q", job.Turns[1].Text)
	}
	// transcript keeps demo linkage end to end
	tr := translate.BuildTranscript(job)
	joined := ""
	for _, e := range tr {
		if arr, ok := e.Value.([][]string); ok {
			for _, in := range arr {
				for _, s := range in {
					joined += s + "\n"
				}
			}
		}
	}
	if !strings.Contains(joined, "call_demo") || !strings.Contains(joined, "real task") {
		t.Fatalf("transcript lost demo/real: %q", joined)
	}
}

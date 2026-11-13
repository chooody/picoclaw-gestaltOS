package agent

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeInTurnKeepsTheSystemPromptStable(t *testing.T) {
	dir := setupWorkspace(t, map[string]string{"AGENT.md": "# Agent\nTest agent."})
	defer os.RemoveAll(dir)
	cb := NewContextBuilder(dir)

	req := PromptBuildRequest{CurrentMessage: "hello", Channel: "pico", ChatID: "pico:proj-web", RuntimeInTurn: true}
	msgs := cb.BuildMessagesFromPrompt(req)
	sys, last := msgs[0], msgs[len(msgs)-1]
	if strings.Contains(sys.Content, "Current Time") {
		t.Fatalf("system prompt still carries the runtime block")
	}
	if !strings.Contains(last.Content, "Current Time") || !strings.HasSuffix(last.Content, "hello") {
		t.Fatalf("user message = %q, want runtime block then the message", last.Content)
	}

	req.RuntimeInTurn = false
	if msgs := cb.BuildMessagesFromPrompt(req); !strings.Contains(msgs[0].Content, "Current Time") {
		t.Fatalf("default layout lost the runtime block")
	}
}

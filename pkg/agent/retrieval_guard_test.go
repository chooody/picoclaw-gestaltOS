package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func rgCall(g *RetrievalGuard, turn, agent, tool string, args map[string]any) HookAction {
	_, d, _ := g.BeforeTool(context.Background(), &ToolCallHookRequest{
		Meta: HookMeta{TurnID: turn, AgentID: agent}, Tool: tool, Arguments: args,
	})
	return d.normalizedAction()
}

func rgPacket(g *RetrievalGuard, turn, agent, tool string, isErr bool) {
	_, _, _ = g.AfterTool(context.Background(), &ToolResultHookResponse{
		Meta: HookMeta{TurnID: turn, AgentID: agent}, Tool: tool,
		Result: &tools.ToolResult{ForLLM: "{}", IsError: isErr},
	})
}

func TestRetrievalGuardCatalogueFirstThenCapped(t *testing.T) {
	g := NewRetrievalGuard(RetrievalGuardConfig{})
	if rgCall(g, "t1", "worker", "read_file", map[string]any{"path": "a.py"}) != HookActionDenyTool {
		t.Fatal("a read before any packet must be denied")
	}
	if rgCall(g, "t1", "worker", "exec", map[string]any{"command": "grep -rn foo ."}) != HookActionDenyTool {
		t.Fatal("grep through exec is a lookup too")
	}
	if rgCall(g, "t1", "worker", "exec", map[string]any{"command": "go test ./..."}) != HookActionContinue {
		t.Fatal("running tests is not a lookup")
	}
	if rgCall(g, "t1", "worker", "edit_file", nil) != HookActionContinue {
		t.Fatal("edits are never gated")
	}
	rgPacket(g, "t1", "worker", "mcp_catalogue_catalogue_packet", true)
	if rgCall(g, "t1", "worker", "read_file", nil) != HookActionDenyTool {
		t.Fatal("a failed packet call does not open lookups")
	}
	rgPacket(g, "t1", "worker", "mcp_gateway_catalogue-catalogue_packet", false)
	if rgCall(g, "t1", "worker", "read_file", nil) != HookActionContinue ||
		rgCall(g, "t1", "worker", "exec", map[string]any{"command": "cd src && cat x.py"}) != HookActionContinue {
		t.Fatal("two lookups after a packet pass")
	}
	if rgCall(g, "t1", "worker", "list_dir", nil) != HookActionDenyTool {
		t.Fatal("the third lookup is over the cap")
	}
	if rgCall(g, "t2", "worker", "read_file", nil) != HookActionDenyTool {
		t.Fatal("a new turn needs its own packet")
	}
}

func TestRetrievalGuardSeesABootstrapPacket(t *testing.T) {
	g := NewRetrievalGuard(RetrievalGuardConfig{Mode: "first"})
	_, _, _ = g.BeforeLLM(context.Background(), &LLMHookRequest{
		Meta:     HookMeta{TurnID: "t1", AgentID: "worker"},
		Messages: []providers.Message{{Role: "user", Content: "Task TI-1\nCatalogue packet for this task. Verbatim..."}},
	})
	for i := 0; i < 5; i++ {
		if rgCall(g, "t1", "worker", "read_file", nil) != HookActionContinue {
			t.Fatal("first mode: unlimited lookups once the bootstrap carries a packet")
		}
	}
}

func TestRetrievalGuardLockedRemovesFileTools(t *testing.T) {
	g := NewRetrievalGuard(RetrievalGuardConfig{Mode: "locked"})
	req := &LLMHookRequest{Meta: HookMeta{TurnID: "t1", AgentID: "worker"}, Tools: []providers.ToolDefinition{
		{Function: providers.ToolFunctionDefinition{Name: "read_file"}},
		{Function: providers.ToolFunctionDefinition{Name: "mcp_catalogue_catalogue_packet"}},
		{Function: providers.ToolFunctionDefinition{Name: "exec"}},
	}}
	out, d, _ := g.BeforeLLM(context.Background(), req)
	if d.Action != HookActionModify || len(out.Tools) != 2 {
		t.Fatalf("locked mode should drop read_file, got %+v", out.Tools)
	}
	rgPacket(g, "t1", "worker", "mcp_catalogue_catalogue_packet", false)
	if rgCall(g, "t1", "worker", "exec", map[string]any{"command": "rg foo"}) != HookActionDenyTool {
		t.Fatal("locked: exec lookups stay denied even after a packet")
	}
}

func TestRetrievalGuardOnlyItsAgents(t *testing.T) {
	g := NewRetrievalGuard(RetrievalGuardConfig{Agents: []string{"worker"}})
	if rgCall(g, "t1", "main", "read_file", nil) != HookActionContinue {
		t.Fatal("agents not listed are not gated")
	}
	if rgCall(g, "t1", "worker", "read_file", nil) != HookActionDenyTool {
		t.Fatal("listed agents are")
	}
}

func TestRetrievalGuardConfigFromSpec(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"mode": "capped", "max_lookups": 1, "agents": []string{"worker"}})
	h, err := newRetrievalGuardFromSpec(context.Background(), config.BuiltinHookConfig{Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	g := h.(*RetrievalGuard)
	if g.max != 1 || !g.agents["worker"] {
		t.Fatalf("config not applied: %+v", g)
	}
	bad, _ := json.Marshal(map[string]any{"mode": "strict"})
	if _, err := newRetrievalGuardFromSpec(context.Background(), config.BuiltinHookConfig{Config: bad}); err == nil {
		t.Fatal("unknown mode must be refused")
	}
}

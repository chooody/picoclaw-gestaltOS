package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

// RetrievalGuardHookName is the builtin hook name used in config
// (hooks.builtins.retrieval_guard).
const RetrievalGuardHookName = "retrieval_guard"

const retrievalGuardMaxTurns = 512

// RetrievalGuardConfig binds agents to the catalogue engine for looking things up.
//
// Measured on the catalogue engine's agent A/B (56 hard tasks, same model): an agent that keeps
// grep and read beside the catalogue mostly goes back to them and re-reads what a packet already
// gave it, whatever its prompt says; taking the file lookups away raised the pass rate from 0.68 to
// 0.84 (packet only) or 0.80 (packet plus two searches and one fetch). This hook is how a host
// applies that without touching each agent's tool list.
type RetrievalGuardConfig struct {
	// Mode: "first" lets file lookups through only after the turn has a catalogue packet (one it
	// fetched, or one the context builder put in its bootstrap); "capped" also allows at most
	// MaxLookups file lookups per turn after that; "locked" removes file lookups altogether.
	// Default "capped".
	Mode string `json:"mode"`
	// MaxLookups is the per-turn file lookup budget in "capped" mode. Default 2.
	MaxLookups int `json:"max_lookups"`
	// Agents the guard applies to (agent ids); empty means every agent.
	Agents []string `json:"agents"`
	// FileTools are lookup tools (default read_file, list_dir). Edit and write tools are never
	// gated: changing a file the packet showed is the point of the work.
	FileTools []string `json:"file_tools"`
	// ExecLookups also counts exec calls whose command is a lookup (grep, rg, find, cat, head,
	// tail, less, ls, tree, sed -n, awk, git grep/show/log -p) as file lookups. Default true.
	ExecLookups *bool `json:"exec_lookups"`
	// PacketMarker is text that, found in the turn's messages, means the bootstrap already carries
	// a packet. Default: the context builder's "Catalogue packet for this task".
	PacketMarker string `json:"packet_marker"`
}

// RetrievalGuard is the retrieval_guard builtin hook.
type RetrievalGuard struct {
	mode   string
	max    int
	agents map[string]bool
	files  map[string]bool
	exec   bool
	marker string
	mu     sync.Mutex
	turns  map[string]*retrievalTurn
	order  []string
}

type retrievalTurn struct {
	packet  bool // the turn has a catalogue packet
	lookups int  // file lookups allowed so far
}

var execLookup = regexp.MustCompile(
	`^\s*(?:cd\s+\S+\s*(?:&&|;)\s*)?(?:grep|egrep|fgrep|rg|ag|ack|find|fd|cat|bat|head|tail|less|more|ls|tree|wc|awk|sed\s+-n|git\s+(?:grep|show|log\s+-p|blame|ls-files))\b`)

func init() {
	_ = RegisterBuiltinHook(RetrievalGuardHookName, newRetrievalGuardFromSpec)
}

func newRetrievalGuardFromSpec(_ context.Context, spec config.BuiltinHookConfig) (any, error) {
	cfg := RetrievalGuardConfig{}
	if len(spec.Config) > 0 {
		if err := json.Unmarshal(spec.Config, &cfg); err != nil {
			return nil, fmt.Errorf("retrieval_guard config: %w", err)
		}
	}
	switch cfg.Mode {
	case "", "first", "capped", "locked":
	default:
		return nil, fmt.Errorf("retrieval_guard mode %q: use first, capped or locked", cfg.Mode)
	}
	return NewRetrievalGuard(cfg), nil
}

// NewRetrievalGuard builds the hook with defaults filled in.
func NewRetrievalGuard(cfg RetrievalGuardConfig) *RetrievalGuard {
	if cfg.Mode == "" {
		cfg.Mode = "capped"
	}
	if cfg.MaxLookups <= 0 {
		cfg.MaxLookups = 2
	}
	if len(cfg.FileTools) == 0 {
		cfg.FileTools = []string{"read_file", "list_dir"}
	}
	if cfg.PacketMarker == "" {
		cfg.PacketMarker = "Catalogue packet for this task"
	}
	g := &RetrievalGuard{
		mode:   cfg.Mode,
		max:    cfg.MaxLookups,
		agents: map[string]bool{},
		files:  map[string]bool{},
		exec:   cfg.ExecLookups == nil || *cfg.ExecLookups,
		marker: cfg.PacketMarker,
		turns:  map[string]*retrievalTurn{},
	}
	for _, a := range cfg.Agents {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			g.agents[a] = true
		}
	}
	for _, t := range cfg.FileTools {
		g.files[t] = true
	}
	return g
}

func (g *RetrievalGuard) applies(meta HookMeta) bool {
	return meta.TurnID != "" && (len(g.agents) == 0 || g.agents[strings.ToLower(strings.TrimSpace(meta.AgentID))])
}

// isPacketTool matches the catalogue's packet tools however the MCP server is named
// (mcp_catalogue_catalogue_packet, mcp_gateway_catalogue-catalogue_packet_batch, ...).
func isPacketTool(name string) bool {
	return strings.Contains(name, "catalogue_packet")
}

func (g *RetrievalGuard) isLookup(tool string, args map[string]any) bool {
	if g.files[tool] {
		return true
	}
	if g.exec && tool == "exec" {
		cmd, _ := args["command"].(string)
		return execLookup.MatchString(cmd)
	}
	return false
}

// turn returns a turn's state, creating it. Callers hold g.mu.
func (g *RetrievalGuard) turn(id string) *retrievalTurn {
	t, ok := g.turns[id]
	if !ok {
		t = &retrievalTurn{}
		g.turns[id] = t
		g.order = append(g.order, id)
		if len(g.order) > retrievalGuardMaxTurns {
			delete(g.turns, g.order[0])
			g.order = g.order[1:]
		}
	}
	return t
}

// BeforeLLM notes a packet already in the bootstrap and, in locked mode, removes the file tools.
func (g *RetrievalGuard) BeforeLLM(_ context.Context, req *LLMHookRequest) (*LLMHookRequest, HookDecision, error) {
	if req == nil || !g.applies(req.Meta) {
		return req, HookDecision{Action: HookActionContinue}, nil
	}
	g.mu.Lock()
	t := g.turn(req.Meta.TurnID)
	if !t.packet {
		for _, m := range req.Messages {
			if m.Role != "assistant" && strings.Contains(m.Content, g.marker) {
				t.packet = true
				break
			}
		}
	}
	g.mu.Unlock()
	if g.mode != "locked" {
		return req, HookDecision{Action: HookActionContinue}, nil
	}
	kept := req.Tools[:0:0]
	for _, def := range req.Tools {
		if !g.files[def.Function.Name] {
			kept = append(kept, def)
		}
	}
	if len(kept) == len(req.Tools) {
		return req, HookDecision{Action: HookActionContinue}, nil
	}
	req.Tools = kept
	return req, HookDecision{Action: HookActionModify}, nil
}

func (g *RetrievalGuard) AfterLLM(_ context.Context, resp *LLMHookResponse) (*LLMHookResponse, HookDecision, error) {
	return resp, HookDecision{Action: HookActionContinue}, nil
}

// BeforeTool denies file lookups that the mode does not allow, with a reason that says what to do.
func (g *RetrievalGuard) BeforeTool(_ context.Context, call *ToolCallHookRequest) (*ToolCallHookRequest, HookDecision, error) {
	if call == nil || !g.applies(call.Meta) || !g.isLookup(call.Tool, call.Arguments) {
		return call, HookDecision{Action: HookActionContinue}, nil
	}
	if g.mode == "locked" {
		return call, HookDecision{Action: HookActionDenyTool, Reason: "[retrieval guard] File lookups are off for " +
			"this agent: get source from the catalogue (catalogue_packet, or catalogue_packet_batch for several " +
			"questions). Its excerpts are verbatim files."}, nil
	}
	g.mu.Lock()
	t := g.turn(call.Meta.TurnID)
	if !t.packet {
		g.mu.Unlock()
		return call, HookDecision{Action: HookActionDenyTool, Reason: "[retrieval guard] Ask the catalogue first: " +
			"call catalogue_packet with the task. Read files only for what its packet does not show."}, nil
	}
	if g.mode == "capped" && t.lookups >= g.max {
		g.mu.Unlock()
		return call, HookDecision{Action: HookActionDenyTool, Reason: fmt.Sprintf("[retrieval guard] This turn "+
			"has used its %d file lookups. Packet excerpts are files you have already read; for a gap, ask "+
			"catalogue_packet again with a narrower task, or answer from what you have and name the gap.", g.max)}, nil
	}
	t.lookups++
	g.mu.Unlock()
	return call, HookDecision{Action: HookActionContinue}, nil
}

// AfterTool marks the turn as having a packet once a packet tool answered without error.
func (g *RetrievalGuard) AfterTool(_ context.Context, result *ToolResultHookResponse) (*ToolResultHookResponse, HookDecision, error) {
	if result != nil && result.Result != nil && !result.Result.IsError && isPacketTool(result.Tool) && g.applies(result.Meta) {
		g.mu.Lock()
		g.turn(result.Meta.TurnID).packet = true
		g.mu.Unlock()
	}
	return result, HookDecision{Action: HookActionContinue}, nil
}

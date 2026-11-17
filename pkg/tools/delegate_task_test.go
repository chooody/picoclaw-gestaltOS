package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fakePrepareTool struct {
	got    map[string]any
	result *ToolResult
}

func (f *fakePrepareTool) Name() string               { return "mcp_bridge_delegate_prepare" }
func (f *fakePrepareTool) Description() string        { return "prepare" }
func (f *fakePrepareTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (f *fakePrepareTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	f.got = args
	return f.result
}

type fakeSpawner struct{ cfgs chan SubTurnConfig }

func (f *fakeSpawner) SpawnSubTurn(_ context.Context, cfg SubTurnConfig) (*ToolResult, error) {
	f.cfgs <- cfg
	return &ToolResult{ForLLM: "worker done"}, nil
}

func TestDelegateTaskSpawnsTheWorkerWithExactlyTheBuiltPrompt(t *testing.T) {
	prompt := "Task TI-7f3a: audit.\nbundle_id CB-0123456789 (pass unchanged)"
	prep := &fakePrepareTool{result: &ToolResult{ForLLM: `{"prompt":` + quote(prompt) +
		`,"ticket_id":"TI-7f3a","bundle_id":"CB-0123456789","procedure_id":"proc.qa.x"}`}}
	sp := &fakeSpawner{cfgs: make(chan SubTurnConfig, 1)}
	tool := NewDelegateTaskTool(
		nil,
		func(name string) (Tool, bool) { return prep, name == prep.Name() },
		prep.Name(),
		"worker",
	)
	tool.SetSpawner(sp)

	done := make(chan *ToolResult, 1)
	res := tool.ExecuteAsync(context.Background(), map[string]any{
		"problem":          "Check the heartbeat files.",
		"resources":        []any{"/root/.picoclaw/workspace/HEARTBEAT.md"},
		"success_criteria": []any{"both files read", "sizes reported"},
	}, func(_ context.Context, r *ToolResult) { done <- r })
	if res.IsError || !res.Async || !strings.Contains(res.ForLLM, "TI-7f3a") ||
		!strings.Contains(res.ForLLM, `agent "worker"`) {
		t.Fatalf("result = %+v", res)
	}
	if prep.got["problem"] != "Check the heartbeat files." || len(prep.got["success_criteria"].([]string)) != 2 ||
		prep.got["resources"].([]string)[0] != "/root/.picoclaw/workspace/HEARTBEAT.md" {
		t.Fatalf("prepare args = %#v", prep.got)
	}
	select {
	case cfg := <-sp.cfgs:
		if cfg.SystemPrompt != prompt || cfg.TargetAgentID != "worker" || cfg.Label != "TI-7f3a" || !cfg.Async {
			t.Fatalf("spawn cfg = %+v", cfg)
		}
	case <-time.After(time.Second):
		t.Fatal("worker not spawned")
	}
	select {
	case r := <-done:
		if r.ForLLM != "worker done" {
			t.Fatalf("callback result = %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("no callback")
	}
}

func TestDelegateTaskPostsToPrepareAndDoesNotLookUpMCP(t *testing.T) {
	var gotPath, gotTool, gotAuth string
	var gotArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &rpc)
		gotTool = rpc.Params.Name
		gotArgs = rpc.Params.Arguments
		if rpc.Method != "tools/call" {
			t.Errorf("method %s", rpc.Method)
		}
		text, _ := json.Marshal(map[string]string{
			"prompt": "built prompt", "ticket_id": "TI-outer", "bundle_id": "CB-outer",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"isError": false,
				"content": []map[string]string{{"type": "text", "text": string(text)}},
			},
		})
	}))
	defer srv.Close()

	sp := &fakeSpawner{cfgs: make(chan SubTurnConfig, 1)}
	tool := NewDelegateTaskTool(nil, func(string) (Tool, bool) {
		t.Error("delegate_task looked up an in-process tool")
		return nil, false
	}, "mcp_gestalt_delegate_prepare", "worker")
	tool.SetPrepareHTTP(srv.URL+"/mcp", "test-token")
	tool.SetSpawner(sp)

	res := tool.ExecuteAsync(context.Background(), map[string]any{
		"problem": "probe the outer handoff", "success_criteria": []any{"prepare was called"},
	}, nil)
	if res.IsError || !strings.Contains(res.ForLLM, "TI-outer") {
		t.Fatalf("result: %+v", res)
	}
	if gotPath != "/mcp" || gotTool != "delegate_prepare" {
		t.Fatalf("posted %s tool %s", gotPath, gotTool)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatal("prepare bearer was not sent")
	}
	if gotArgs["problem"] != "probe the outer handoff" {
		t.Fatalf("args %#v", gotArgs)
	}
	select {
	case cfg := <-sp.cfgs:
		if cfg.SystemPrompt != "built prompt" {
			t.Fatalf("worker prompt %q", cfg.SystemPrompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker was not started")
	}
}

func TestDelegateTaskLivePrepare(t *testing.T) {
	if os.Getenv("GESTALT_DELEGATE_LIVE") != "1" {
		t.Skip("set GESTALT_DELEGATE_LIVE=1 to post one brief at the running gestalt-prepare")
	}
	url := os.Getenv("GESTALT_PREPARE_URL")
	if url == "" {
		url = "http://127.0.0.1:38496/mcp"
	}
	sp := &fakeSpawner{cfgs: make(chan SubTurnConfig, 1)}
	tool := NewDelegateTaskTool(nil, func(string) (Tool, bool) {
		t.Error("delegate_task looked up an in-process tool")
		return nil, false
	}, "", "worker")
	tool.SetPrepareHTTP(url, os.Getenv("GESTALT_PREPARE_TOKEN"))
	tool.SetSpawner(sp)
	res := tool.ExecuteAsync(context.Background(), map[string]any{
		"problem":          "Delegate seam probe. Do not change files.",
		"resources":        []any{"gestaltos/picoclaw/prepare/prepare_server.py"},
		"success_criteria": []any{"the prompt names a ticket id"},
		"label":            "Delegate seam probe",
	}, nil)
	if res.IsError {
		t.Fatal(res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "ticket ") {
		t.Fatalf("result did not name a ticket: %s", res.ForLLM)
	}
	select {
	case cfg := <-sp.cfgs:
		if !strings.Contains(cfg.SystemPrompt, "TI-") {
			t.Fatal("worker prompt has no ticket id")
		}
		t.Logf("outer prepare url %s ticket line in result", url)
	case <-time.After(5 * time.Second):
		t.Fatal("worker was not started from the live prepare result")
	}
}

func TestDelegateTaskErrors(t *testing.T) {
	sp := &fakeSpawner{cfgs: make(chan SubTurnConfig, 1)}
	cases := map[string]struct {
		prep *fakePrepareTool
		args map[string]any
		want string
	}{
		"no problem":      {nil, map[string]any{"success_criteria": []any{"x"}}, "problem is required"},
		"no criteria":     {nil, map[string]any{"problem": "p"}, "success_criteria"},
		"prepare missing": {nil, map[string]any{"problem": "p", "success_criteria": "x"}, "not available"},
		"prepare failed": {
			&fakePrepareTool{result: ErrorResult("ticket engine down")},
			map[string]any{"problem": "p", "success_criteria": "x"},
			"ticket engine down",
		},
		"no prompt in json": {
			&fakePrepareTool{result: &ToolResult{ForLLM: `{"ticket_id":"TI-1"}`}},
			map[string]any{"problem": "p", "success_criteria": "x"},
			"no prompt",
		},
	}
	for name, c := range cases {
		lookup := func(string) (Tool, bool) { return nil, false }
		if c.prep != nil {
			p := c.prep
			lookup = func(string) (Tool, bool) { return p, true }
		}
		tool := NewDelegateTaskTool(nil, lookup, "mcp_bridge_delegate_prepare", "worker")
		tool.SetSpawner(sp)
		res := tool.Execute(context.Background(), c.args)
		if !res.IsError || !strings.Contains(res.ForLLM, c.want) {
			t.Errorf("%s: result = %+v, want error containing %q", name, res, c.want)
		}
	}
	if len(sp.cfgs) != 0 {
		t.Fatal("no worker may start when preparing fails")
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

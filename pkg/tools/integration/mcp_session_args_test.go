package integrationtools

import (
	"context"
	"testing"

	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

func TestSessionArgsNameTheRootSession(t *testing.T) {
	ctx := toolshared.WithToolSessionContext(context.Background(), "worker", "subturn-3", nil)
	ctx = toolshared.WithToolRootSession(ctx, "agent:main:pico:direct:pico:proj-web")
	in := map[string]any{"problem": "x"}
	out := withSessionArgs(ctx, in)
	if out["_session"] != "agent:main:pico:direct:pico:proj-web" || out["_agent"] != "worker" || out["problem"] != "x" {
		t.Fatalf("got %v", out)
	}
	if _, ok := in["_session"]; ok {
		t.Fatalf("caller's args were modified")
	}
	plain := toolshared.WithToolSessionContext(context.Background(), "main", "pico:assist-web", nil)
	if got := withSessionArgs(plain, nil)["_session"]; got != "pico:assist-web" {
		t.Fatalf("fallback _session = %v", got)
	}
}

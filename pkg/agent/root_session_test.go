package agent

import "testing"

func TestRootSessionKeyFollowsParents(t *testing.T) {
	root := &turnState{sessionKey: "pico:proj-web"}
	mid := &turnState{sessionKey: "subturn-1", parentTurnState: root}
	leaf := &turnState{sessionKey: "subturn-2", parentTurnState: mid}
	for _, ts := range []*turnState{root, mid, leaf} {
		if got := ts.rootSessionKey(); got != "pico:proj-web" {
			t.Fatalf("rootSessionKey() = %q", got)
		}
	}
}

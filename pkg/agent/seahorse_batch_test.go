package agent

import (
	"encoding/json"
	"testing"
)

func TestParseSeahorseBatch(t *testing.T) {
	if b := parseSeahorseBatch(nil); b.Trigger != 0 {
		t.Fatalf("no config: %+v, want off", b)
	}
	b := parseSeahorseBatch(json.RawMessage(`{"batch":{"trigger":0.7,"target":0.4}}`))
	if b.Trigger != 0.7 || b.Target != 0.4 {
		t.Fatalf("got %+v", b)
	}
	b = parseSeahorseBatch(json.RawMessage(`{"batch":{"trigger":0.8,"target":0.9}}`))
	if b.Target >= b.Trigger {
		t.Fatalf("target above trigger not corrected: %+v", b)
	}
}

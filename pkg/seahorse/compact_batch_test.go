package seahorse

import (
	"context"
	"testing"
)

func TestCompactBatchWaitsForTriggerThenCompactsKeepingTheTail(t *testing.T) {
	ce, s, convID := newTestCompactionEngine(t)
	ctx := context.Background()
	for i := 0; i < FreshTailCount+3*LeafMinFanout; i++ {
		m, _ := s.AddMessage(ctx, convID, "user", "message", 100)
		s.AppendContextMessage(ctx, convID, m.ID)
	}
	before, _ := s.GetContextTokenCount(ctx, convID)

	res, err := ce.CompactBatch(ctx, convID, before+1, 0)
	if err != nil || len(res.SummariesCreated) != 0 {
		t.Fatalf("below trigger: res=%+v err=%v, want no compaction", res, err)
	}

	res, err = ce.CompactBatch(ctx, convID, before, before/2)
	if err != nil {
		t.Fatalf("CompactBatch: %v", err)
	}
	if res.LeafSummaries == 0 {
		t.Fatalf("over trigger: no leaf summaries")
	}
	items, _ := s.GetContextItems(ctx, convID)
	tail := 0
	for i := len(items) - 1; i >= 0 && items[i].ItemType == "message"; i-- {
		tail++
	}
	if tail < FreshTailCount {
		t.Fatalf("fresh tail = %d messages, want >= %d", tail, FreshTailCount)
	}
}

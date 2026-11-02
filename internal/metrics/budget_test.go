package metrics

import (
	"fmt"
	"testing"
)

func TestGlobalBudgetPreservesCompletedChatTotals(t *testing.T) {
	s := newTestState()
	for turn := 0; turn < 20; turn++ {
		id := fmt.Sprint("turn-", turn)
		start(s, id)
		for step := 0; step < 500; step++ {
			usage(s, id, fmt.Sprint("response-", step), 1000, 500, 100)
		}
		record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": id})
	}
	x := s.Snapshot()
	if x.Turns != 20 || x.Steps != 10000 || !x.CountsComplete || !s.ValidateBudget() {
		t.Fatalf("compaction lost completed totals: %+v", x)
	}
	if s.Turns["turn-0"] == nil {
		t.Fatal("budget did not compact an older completed turn")
	}
	usage(s, "turn-0", "late-unknown-response", 1000, 500, 100)
	if x = s.Snapshot(); x.Steps != 10001 || !x.CountsComplete || x.TimingComplete {
		t.Fatal("late compacted history was silently counted or presented as complete")
	}
}

func TestUnknownRecordPayloadCannotInvalidateAccounting(t *testing.T) {
	s := newTestState()
	s.Observe([]byte(`{"type":"response_item","payload":"ignored content"}`))
	if s.Malformed != 0 || !s.HistoryComplete {
		t.Fatal("irrelevant valid record damaged accounting history")
	}
}

func TestActiveTurnCountsHaveNoResponseDetailLimit(t *testing.T) {
	s := newTestState()
	start(s, "long-turn")
	for i := 0; i < 10000; i++ {
		usage(s, "long-turn", fmt.Sprint("r-", i), int64(i+100), 50, int64(i+10))
	}
	x := s.Snapshot()
	if !x.CountsComplete || x.Steps != 10000 || !s.ValidateBudget() {
		t.Fatal("counting depended on timing retention", x)
	}
}

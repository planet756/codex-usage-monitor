package metrics

import "testing"

func TestLateRetainedAccountingCannotDowngradeConfirmedUsage(t *testing.T) {
	s := newTestState()
	start(s, "a")
	usage(s, "a", "a1", 100, 50, 10)
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "a"})
	start(s, "b")
	usage(s, "b", "b1", 200, 100, 20)
	usage(s, "a", "late-a2", 110, 50, 11)
	if s.Snapshot().Total != 220 || s.CurrentID != "b" || s.Snapshot().Steps != 3 {
		t.Fatal("late retained accounting regressed tokens or the active turn")
	}
}

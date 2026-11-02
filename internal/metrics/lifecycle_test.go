package metrics

import "testing"

func TestLateRetainedStartCannotChangeActiveTurn(t *testing.T) {
	s := newTestState()
	start(s, "a")
	usage(s, "a", "a1", 100, 50, 10)
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "a"})
	start(s, "b")
	item(s, "b", "AgentMessage", 3000, 4000)
	before := s.Snapshot()
	start(s, "a")
	if s.CurrentID != "b" || s.Snapshot().Status != before.Status {
		t.Fatal("late completed start replaced the active turn")
	}
	usage(s, "b", "b1", 200, 100, 20)
	if s.Snapshot().Turns != 2 || s.Snapshot().Steps != 2 {
		t.Fatal("explicit accounting entered the older completed turn")
	}
	start(s, "b")
	if s.CurrentID != "b" || s.Snapshot().Turns != 2 {
		t.Fatal("duplicate current start changed settled counts")
	}
}

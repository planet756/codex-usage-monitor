package metrics

import (
	"fmt"
	"testing"
)

func TestChatCountsDeduplicateResponsesAndSurviveEviction(t *testing.T) {
	s := newTestState()
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("turn-%d", i)
		start(s, id)
		start(s, id)
		for j := 0; j < 5; j++ {
			responseID := fmt.Sprintf("r%d", j)
			item(s, id, "Reasoning", 1000, 2000)
			item(s, id, "AgentMessage", 2000, 3000)
			item(s, id, "McpToolCall", 3000, 4000)
			response(s, id, responseID, 100, 0, 5000)
			response(s, id, responseID, 100, 0, 5000)
		}
	}
	x := s.Snapshot()
	if x.Turns != 70 || x.Steps != 350 || !x.HasCounts || !x.CountsComplete || len(s.Turns) != 32 {
		t.Fatalf("counted tools/items, lost old turns, or duplicated a response: %+v", x)
	}
	start(s, "pending")
	if s.Snapshot().Turns != 70 || newTestState().Snapshot().Steps != 0 {
		t.Fatal("counted an unsettled response or leaked another chat's counts")
	}
	s.MarkIncomplete()
	if s.Snapshot().CountsComplete || s.Snapshot().Steps != 350 {
		t.Fatal("partial history must preserve and qualify its counts")
	}
}

func TestMissingResponseIdentityCannotFabricateCounts(t *testing.T) {
	s := newTestState()
	start(s, "a")
	record(s, "token_usage_record", map[string]any{"turn_id": "a"})
	x := s.Snapshot()
	if x.Steps != 0 || x.Turns != 0 || x.CountsComplete {
		t.Fatalf("unidentified response counted: %+v", x)
	}
}

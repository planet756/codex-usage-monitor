package metrics

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func response(s *fixtureState, turn, id string, output, reasoning, end int64) {
	b, _ := json.Marshal(map[string]any{
		"type": "token_usage_record", "timestamp": time.UnixMilli(end).UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{"turn_id": turn, "response_id": id,
			"usage": Usage{Input: 100, Output: output, Reasoning: reasoning}},
	})
	s.Observe(b)
}

func TestSessionDecodeUsesWeightedResponseTotalsAndPersistsBetweenTurns(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "a", "Reasoning", 1000, 2400)
	item(s, "a", "AgentMessage", 2400, 2900)
	response(s, "a", "r1", 100, 60, 3000)
	response(s, "a", "r1", 100, 60, 3000)
	x := s.Snapshot()
	if x.TPS == nil || *x.TPS != 50 || !x.TPSApproximate || x.DecodeTokens != 100 || x.DecodeSamples != 1 {
		t.Fatalf("first response or reasoning was counted incorrectly: %+v", x)
	}
	item(s, "a", "McpToolCall", 3000, 99000)
	// A duplicated old item must not contaminate the next response's timing.
	item(s, "a", "AgentMessage", 2400, 2900)
	item(s, "a", "AgentMessage", 100000, 100900)
	response(s, "a", "r2", 300, 0, 101000)
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "a"})
	start(s, "b")
	x = s.Snapshot()
	if x.TPS == nil || math.Abs(*x.TPS-400.0/3) > 1e-9 || x.DecodeMS != 3000 || x.DecodeSamples != 2 || x.ToolMS != 96000 {
		t.Fatalf("used mean TPS, tool wait, or reset on idle/new turn: %+v", x)
	}
	// Cancelled partial work changes neither side of the cumulative quotient.
	item(s, "b", "Reasoning", 110000, 115000)
	record(s, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "b"})
	if got := s.Snapshot(); *got.TPS != *x.TPS || got.DecodeTokens != 400 {
		t.Fatal("an unaccounted interrupted response changed session speed")
	}
	if newTestState().Snapshot().TPS != nil {
		t.Fatal("another chat inherited a rate")
	}
}

func TestDecodeSkipsIncompleteUnpairedAndUnidentifiedResponses(t *testing.T) {
	s := newTestState()
	start(s, "a")
	response(s, "a", "no-output-time", 100, 0, 1000)
	item(s, "a", "AgentMessage", 2000, 3000)
	// Missing end timestamp and missing ID both close and invalidate a window.
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "missing-end", "usage": Usage{Input: 100, Output: 100}})
	item(s, "a", "AgentMessage", 4000, 5000)
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "usage": Usage{Input: 100, Output: 100}})
	response(s, "a", "after-unidentified", 100, 0, 6000)
	item(s, "a", "AgentMessage", 7000, 8000)
	response(s, "a", "reversed-end", 100, 0, 7500)
	if got := s.Snapshot(); got.TPS != nil || got.DecodeSkipped != 5 {
		t.Fatalf("a missing boundary was fabricated: %+v", got)
	}
	item(s, "a", "AgentMessage", 9000, 10000)
	response(s, "a", "valid", 100, 0, 10000)
	// This response re-establishes an end boundary after the reversed one;
	// only the following response can provide a safely paired sample.
	item(s, "a", "AgentMessage", 11000, 12000)
	response(s, "a", "after-recovery", 100, 0, 12000)
	s.MarkIncomplete()
	response(s, "a", "after-gap", 100, 0, 13000)
	x := s.Snapshot()
	if x.TPS == nil || *x.TPS != 100 || x.DecodeSamples != 1 || x.DecodeSkipped != 7 || x.HistoryComplete {
		t.Fatalf("gap erased prior samples or fabricated new ones: %+v", x)
	}
}

func TestDecodeTotalsSurviveRecentTurnEviction(t *testing.T) {
	s := newTestState()
	for i := int64(1); i <= 70; i++ {
		id := time.Unix(i, 0).String()
		start(s, id)
		item(s, id, "AgentMessage", i*10000, i*10000+1000)
		response(s, id, id, 100, 0, i*10000+1000)
	}
	x := s.Snapshot()
	if len(s.Turns) != 32 || x.DecodeSamples != 70 || x.DecodeTokens != 7000 || *x.TPS != 100 {
		t.Fatalf("session totals lost with old turn state: %+v", x)
	}
}

func TestOtherThreadMetadataCannotEnterCurrentChat(t *testing.T) {
	s := newTestState()
	record(s, "session_meta", map[string]any{"id": "chat-a"})
	record(s, "token_usage_record", map[string]any{"thread_id": "chat-b", "turn_id": "b", "response_id": "foreign",
		"thread_token_usage": Usage{Input: 100, Output: 100}})
	if s.HasUsage || s.DecodeSkipped != 0 || len(s.Turns) != 0 || s.TurnCount != 0 || s.StepCount != 0 {
		t.Fatal("another thread entered the current chat")
	}
}

func TestMissingEarlyItemTimeDoesNotCreditLaterTextWithAllResponseTokens(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "a", "AgentMessage", 1000, 2000)
	response(s, "a", "first", 100, 0, 2000)
	item(s, "a", "Reasoning", 0, 4000)
	item(s, "a", "AgentMessage", 4000, 5000)
	response(s, "a", "missing-first", 300, 200, 5000)
	x := s.Snapshot()
	if x.DecodeSamples != 1 || x.DecodeSkipped != 1 || x.DecodeTokens != 100 || *x.TPS != 100 {
		t.Fatalf("missing reasoning timing inflated decode speed: %+v", x)
	}
}

func TestLateDuplicateUsageCannotRegressCountersOrChangeActiveTurn(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "a", "AgentMessage", 1000, 2000)
	response(s, "a", "r1", 100, 0, 2000)
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "accounting-1",
		"thread_token_usage": Usage{Input: 1000, Output: 100}})
	item(s, "a", "AgentMessage", 3000, 4000)
	response(s, "a", "r2", 300, 0, 4000)
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "accounting-2",
		"thread_token_usage": Usage{Input: 2000, Output: 400}})
	start(s, "b")
	item(s, "b", "AgentMessage", 5000, 6000)
	response(s, "a", "r1", 100, 0, 2000)
	if s.CurrentID != "b" || s.Usage.Output != 400 || s.Turns["a"].Incomplete {
		t.Fatal("late duplicate changed counters, turn, or timing eligibility")
	}
	response(s, "b", "r3", 500, 0, 6000)
	x := s.Snapshot()
	if x.DecodeTokens != 900 || x.DecodeSamples != 3 || *x.TPS != 300 {
		t.Fatalf("late duplicate lost or corrupted a newer response: %+v", x)
	}
}

func TestAccountingOnlyRecordCannotInvalidateResponseTiming(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "a", "AgentMessage", 1000, 2000)
	response(s, "a", "r1", 100, 0, 2000)
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "accounting-only", "thread_token_usage": Usage{Input: 10000, Output: 5000}})
	item(s, "a", "AgentMessage", 3000, 4000)
	response(s, "a", "r2", 300, 0, 4000)
	x := s.Snapshot()
	if x.DecodeSamples != 2 || x.DecodeTokens != 400 || *x.TPS != 200 || x.Usage.Output != 5000 || s.Turns["a"].Incomplete {
		t.Fatalf("accounting contaminated timing: %+v", x)
	}
}

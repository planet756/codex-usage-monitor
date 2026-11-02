package metrics

import (
	"math"
	"testing"
)

func TestLatestSettledUsageCanCorrectAnEarlierLargerBaseline(t *testing.T) {
	s := newTestState()
	start(s, "a")
	usage(s, "a", "r1", 900, 800, 90)
	usage(s, "a", "r2", 200, 50, 20)
	x := s.Snapshot()
	if x.Total != 220 || x.CachePercent == nil || *x.CachePercent != 25 || x.Steps != 2 {
		t.Fatalf("kept obsolete larger accounting instead of the latest record: %+v", x)
	}
	usage(s, "a", "r1", 900, 800, 90)
	if s.Snapshot().Total != 220 || s.Snapshot().Steps != 2 {
		t.Fatal("late duplicate replaced the corrected baseline")
	}
	// Missing cache data in a new response is unknown, even if token totals match.
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "r3", "thread_token_usage": map[string]int64{"input_tokens": 200, "output_tokens": 20}})
	if s.Snapshot().CachePercent != nil {
		t.Fatal("borrowed cache metadata from an earlier response")
	}
}

func TestMissingTurnIdentityCannotBorrowTheActiveTurn(t *testing.T) {
	for _, kind := range []string{"task_started", "item_completed", "task_complete", "turn_aborted", "token_usage_record"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestState()
			start(s, "a")
			item(s, "a", "Reasoning", 1000, 2000)
			p := map[string]any{"type": kind, "response_id": "unknown", "started_at_ms": 2000, "completed_at_ms": 3000,
				"item": map[string]any{"type": "AgentMessage"}, "time_to_first_token_ms": 30,
				"thread_token_usage": Usage{Input: 100, Cached: 50, Output: 10}, "usage": Usage{Input: 100, Output: 10}}
			carrier := "event_msg"
			if kind == "token_usage_record" {
				carrier = kind
			}
			record(s, carrier, p)
			x := s.Snapshot()
			if x.Steps != 0 || x.Turns != 0 || x.TTFTCount != 0 || x.ModelMS != 1000 || s.Turns["a"].Complete || x.HistoryComplete || x.CountsComplete {
				t.Fatalf("unidentified accounting borrowed active turn: %+v", x)
			}
			if kind == "token_usage_record" && x.Total != 110 {
				t.Fatal("lost valid whole-thread total")
			}
		})
	}
}

func TestInvalidTimingNeverInflatesDurationsOrTPS(t *testing.T) {
	for _, kind := range []string{"Reasoning", "CommandExecution"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestState()
			start(s, "a")
			item(s, "a", kind, 0, 5000)
			item(s, "a", kind, 6000, 7000)
			response(s, "a", "r", 100, 0, 8000)
			x := s.Snapshot()
			if x.ModelMS+x.ToolMS != 1000 || x.TimingComplete || x.TPS != nil {
				t.Fatalf("missing timestamp counted as epoch zero: %+v", x)
			}
		})
	}
}

func TestNonAccountingItemsDoNotInvalidateStatistics(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "", "UserMessage", 0, -1)
	item(s, "a", "ContextCompaction", 0, -1)
	if !s.HistoryComplete || len(s.Turns) != 1 {
		t.Fatal("irrelevant item timing changed accounting")
	}
}

func TestTimingOverflowCannotWrapTotals(t *testing.T) {
	s := newTestState()
	start(s, "a")
	s.ModelMS, s.ToolMS, s.TTFTSum = math.MaxInt64-10, math.MaxInt64-10, math.MaxInt64-10
	item(s, "a", "Reasoning", 1000, 2000)
	item(s, "a", "CommandExecution", 2000, 3000)
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "a", "time_to_first_token_ms": 100})
	if s.ModelMS != math.MaxInt64-10 || s.ToolMS != math.MaxInt64-10 || s.TTFTSum != math.MaxInt64-10 || s.TTFTCount != 0 || s.TimingComplete {
		t.Fatal("timing overflow published incorrect totals")
	}
}

func TestMissingRecordTypeCannotCertifyCompleteHistory(t *testing.T) {
	for _, line := range []string{"null", `{}`, `{"ordinal":0,"payload":{}}`, `{"type":"event_msg","payload":null}`, `{"type":"session_meta","payload":{}}`} {
		s := newTestState()
		s.Observe([]byte(line))
		if s.HistoryComplete || s.Malformed != 1 {
			t.Fatalf("accepted a record without a carrier type: %s", line)
		}
	}
}

package metrics

import (
	"encoding/json"
	"fmt"
	"testing"
)

func record(s *fixtureState, kind string, p any) {
	b, _ := json.Marshal(map[string]any{"type": kind, "payload": p})
	s.Observe(b)
}
func start(s *fixtureState, id string) {
	record(s, "event_msg", map[string]any{"type": "task_started", "turn_id": id})
}
func item(s *fixtureState, id, kind string, a, b int64) {
	record(s, "event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]string{"type": kind}, "started_at_ms": a, "completed_at_ms": b})
}
func usage(s *fixtureState, id, response string, in, cached, out int64) {
	record(s, "token_usage_record", map[string]any{"turn_id": id, "response_id": response, "thread_token_usage": Usage{Input: in, Cached: cached, Output: out}, "usage": Usage{Input: 10, Output: 10}})
}

func TestUnionOutOfOrderAndOverlap(t *testing.T) {
	var xs []Interval
	var total int64
	for _, x := range []Interval{{5, 10}, {0, 6}, {12, 15}, {10, 12}, {0, 15}, {2, 3}} {
		var d int64
		xs, d = Union(xs, x)
		total += d
	}
	if total != 15 || Length(xs) != 15 || len(xs) != 1 {
		t.Fatalf("union=%v, total=%d", xs, total)
	}
	for _, x := range []Interval{{-1, 2}, {5, 4}, {3, 3}} {
		var d int64
		xs, d = Union(xs, x)
		if d != 0 {
			t.Fatal("invalid duration added")
		}
	}
}

func TestResponseDeduplicationAndSeparateToolTime(t *testing.T) {
	s := newTestState()
	start(s, "a")
	usage(s, "a", "response-1", 100, 80, 10)
	usage(s, "a", "response-1", 100, 80, 10)
	// Same per-response token values, a different response identity.
	usage(s, "a", "response-2", 200, 160, 20)
	item(s, "a", "Reasoning", 1000, 2200)
	item(s, "a", "AgentMessage", 2000, 3000)
	item(s, "a", "CommandExecution", 4000, 10000)
	item(s, "a", "McpToolCall", 8000, 12000)
	x := s.Snapshot()
	if x.Total != 220 || x.Uncached == nil || *x.Uncached != 40 || x.CachePercent == nil || *x.CachePercent != 80 {
		t.Fatalf("bad accounting: %+v", x)
	}
	if x.ModelMS != 2000 || x.ToolMS != 8000 || x.TPS != nil {
		t.Fatalf("bad timing: %+v", x)
	}
	if s.StepCount != 2 || s.DecodeSkipped != 2 {
		t.Fatal("response identity was not deduplicated independently of token values")
	}
}

func TestAccountingWithoutTimingCannotInventTPS(t *testing.T) {
	s := newTestState()
	start(s, "a")
	for i, out := range []int64{1010, 1010, 1020} {
		usage(s, "a", fmt.Sprint(i), 1000+out, 0, out)
	}
	if x := s.Snapshot(); x.TPS != nil || x.Usage.Output != 1020 {
		t.Fatalf("invalid accounting-only snapshot: %+v", x)
	}
}

func TestTTFTDedupAndMissingFields(t *testing.T) {
	s := newTestState()
	for i, ttft := range []any{int64(1000), int64(3000), nil} {
		id := fmt.Sprint(i)
		start(s, id)
		p := map[string]any{"type": "task_complete", "turn_id": id}
		if ttft != nil {
			p["time_to_first_token_ms"] = ttft
		}
		record(s, "event_msg", p)
		record(s, "event_msg", p)
	}
	x := s.Snapshot()
	if x.TTFTCount != 2 || x.AverageTTFT == nil || *x.AverageTTFT != 2000 {
		t.Fatalf("invalid TTFT mean: %+v", x)
	}
}

func TestIncompleteTurnDoesNotInventTPS(t *testing.T) {
	s := newTestState()
	usage(s, "a", "r", 100, 0, 10)
	item(s, "a", "AgentMessage", 1000, 2000)
	if s.Snapshot().TPS != nil {
		t.Fatal("tail without task_started reported TPS")
	}
	start(s, "b")
	usage(s, "b", "r2", 200, 0, 20)
	item(s, "b", "AgentMessage", 3000, 4000)
	s.Observe([]byte(`{"type":`))
	if s.Snapshot().TPS != nil || s.Snapshot().HistoryComplete {
		t.Fatal("corrupt metadata did not invalidate timing")
	}
}

func TestMissingCacheIsUnknownAndMissingCountersAreNotZero(t *testing.T) {
	s := newTestState()
	s.Observe([]byte(`{"type":"token_usage_record","payload":{"turn_id":"a","thread_token_usage":{"input_tokens":100,"output_tokens":20}}}`))
	x := s.Snapshot()
	if !x.HasUsage || x.Total != 120 || x.CachePercent != nil || x.Uncached != nil {
		t.Fatalf("missing cache fabricated a percentage: %+v", x)
	}
	s2 := newTestState()
	s2.Observe([]byte(`{"type":"token_usage_record","payload":{"thread_token_usage":{"total_tokens":120}}}`))
	if s2.Snapshot().HasUsage {
		t.Fatal("missing input/output counters were fabricated as zero")
	}
}

func TestTurnStateBoundedAndHistoryRetained(t *testing.T) {
	s := newTestState()
	for i := range 100 {
		id := fmt.Sprint(i)
		start(s, id)
		item(s, id, "AgentMessage", int64(i*1000+1), int64(i*1000+101))
	}
	if len(s.Turns) > 32 || s.ModelMS != 10000 {
		t.Fatalf("unbounded state or lost history: turns=%d duration=%d", len(s.Turns), s.ModelMS)
	}
}

func FuzzObserve(f *testing.F) {
	f.Add([]byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"a"}}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			t.Skip()
		}
		s := newTestState()
		s.Observe(b)
		_ = s.Snapshot()
	})
}

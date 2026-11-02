package metrics

import (
	"math"
	"testing"
)

func TestAuditMissingResponseTimestampCannotBorrowPreviousOutput(t *testing.T) {
	s := newTestState()
	start(s, "a")
	item(s, "a", "Reasoning", 1000, 2000)
	record(s, "token_usage_record", map[string]any{"turn_id": "a", "response_id": "r1", "usage": Usage{Input: 100, Output: 100}})
	item(s, "a", "AgentMessage", 1900, 2000)
	item(s, "a", "CommandExecution", 2000, 9999)
	item(s, "a", "Reasoning", 10000, 11000)
	response(s, "a", "r2", 100, 0, 11000)
	x := s.Snapshot()
	if x.TPS != nil && math.Abs(*x.TPS-100) > 1e-9 {
		t.Fatalf("old late output contaminated next sample: tps=%.6f decode_ms=%d samples=%d skipped=%d history_complete=%t", *x.TPS, x.DecodeMS, x.DecodeSamples, x.DecodeSkipped, x.HistoryComplete)
	}
}

func TestAuditMissingCumulativeUsageCannotPublishUnqualifiedOldTotal(t *testing.T) {
	s := newTestState()
	start(s, "a")
	usage(s, "a", "r1", 100, 50, 10)
	item(s, "a", "AgentMessage", 2000, 3000)
	response(s, "a", "r2", 20, 0, 3000)
	x := s.Snapshot()
	if x.HasUsage && x.HistoryComplete && x.Total == 110 {
		t.Fatalf("new settled response advanced steps but retained stale total as complete: steps=%d total=%d has_usage=%t history_complete=%t", x.Steps, x.Total, x.HasUsage, x.HistoryComplete)
	}
}

func TestUsageFreshnessRecoveryAndLateOlderRecords(t *testing.T) {
	s := newTestState()
	start(s, "old")
	usage(s, "old", "old-r1", 100, 50, 10)
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "old"})
	start(s, "new")
	usage(s, "new", "new-r1", 200, 100, 20)
	response(s, "old", "late-old", 5, 0, 3000)
	if x := s.Snapshot(); !x.HasUsage || x.Total != 220 {
		t.Fatal("older turn invalidated latest baseline")
	}
	response(s, "new", "new-r2", 20, 0, 5000)
	if x := s.Snapshot(); x.HasUsage || !x.UsageStale || x.CachePercent != nil || x.Uncached != nil {
		t.Fatal("missing latest cumulative usage displayed as current")
	}
	usage(s, "new", "new-r3", 300, 150, 40)
	if x := s.Snapshot(); !x.HasUsage || x.UsageStale || x.Total != 340 {
		t.Fatal("fresh cumulative usage did not recover")
	}
}

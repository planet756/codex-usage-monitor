package presentation

import (
	"testing"

	"codex-usage-monitor/internal/metrics"
)

func TestPartialStatisticsNeverRoundAboveTheirLowerBound(t *testing.T) {
	s := metrics.Snapshot{HasCounts: true, Turns: 1999, Steps: 99999, HasTiming: true, ModelMS: 1999, ToolMS: 59999}
	if got := CompactCounts(s, "en"); got != "≥1.9K turns · ≥99.9K steps" {
		t.Fatal(got)
	}
	if got := tightCounts(s, "en", compactCount(s)); got != "≥1.9K turns · ≥99.9K steps" {
		t.Fatal(got)
	}
	s.Steps = 1999999
	if got := CompactCounts(s, "zh-CN"); got != "≥1.9K 轮 · ≥1.9M 步" {
		t.Fatal(got)
	}
	for _, row := range DetailRows(s, "en", false) {
		if row.Label == "LLM time" && row.Value != "≥ 1.9 s" {
			t.Fatal(row)
		}
		if row.Label == "Tool time" && row.Value != "≥ 59.9 s" {
			t.Fatal(row)
		}
	}
}

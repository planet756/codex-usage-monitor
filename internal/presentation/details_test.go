package presentation

import (
	"codex-usage-monitor/internal/metrics"
	"strings"
	"testing"
)

func TestDetailValuesDistinguishMissingZeroAndTurnTiming(t *testing.T) {
	rows := DetailRows(metrics.Snapshot{}, "en", false)
	for _, row := range rows {
		if row.Value != "-" || row.Unit != "" {
			t.Fatalf("fabricated missing data: %+v", row)
		}
	}
	x := metrics.Snapshot{HasCounts: true, CountsComplete: true}.WithUsage(metrics.Usage{}, true)
	if got := Counts(x, "en"); got != "0 turns · 0 steps" {
		t.Fatal(got)
	}
	rows = DetailRows(x, "en", false)
	if rows[0].Value != "0" || rows[0].Unit != "tok" || rows[1].Value != "-" || rows[4].Value != "0" {
		t.Fatalf("zero input has no cache ratio: %+v", rows)
	}
	x.Turns, x.Steps = 1, 5
	if got := Counts(x, "en"); got != "1 turn · 5 steps" {
		t.Fatal(got)
	}
	x.CountsComplete = false
	if got := Counts(x, "zh-CN"); got != "≥1 轮 · ≥5 步" {
		t.Fatal(got)
	}
	x.TPS = new(208.0)
	x.AverageTTFT = new(1000.0)
	x.DecodeMS = 1000
	rows = DetailRows(x, "en", true)
	if rows[7].Value != "-" || rows[8].Value != "-" {
		t.Fatal("tail seed leaked incomplete timing")
	}
	for _, row := range DetailRows(x, "zh-CN", false) {
		if strings.Contains(row.Label, "Tokens per second") || strings.Contains(row.Label, "Uncached input") {
			t.Fatal("untranslated detail label")
		}
	}
}

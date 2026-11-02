package presentation

import (
	"codex-usage-monitor/internal/metrics"
	"strings"
	"testing"
)

func TestCoverageExplainsValidTotalAndSkippedSamples(t *testing.T) {
	x := metrics.Snapshot{DecodeSamples: 122, DecodeSkipped: 11, HistoryComplete: true, TimingComplete: true}
	if got := SampleCoverage(x, "en"); got != "TPS samples: 122/133 · Skipped: 11" {
		t.Fatal(got)
	}
	if got := SampleCoverage(x, "zh-CN"); got != "TPS 样本：122/133 · 跳过：11" {
		t.Fatal(got)
	}
	x.HistoryComplete = false
	if got := SampleCoverage(x, "en"); !strings.HasSuffix(got, " · Partial history") {
		t.Fatal(got)
	}
	x.DecodeSamples = 0
	if got := SampleCoverage(x, "en"); !strings.Contains(got, "0/11") {
		t.Fatal("missing timing fabricated valid samples")
	}
}

func TestTimingLimitDoesNotClaimHistoryIsMissing(t *testing.T) {
	s := metrics.Snapshot{HasCounts: true, CountsComplete: true, HistoryComplete: true, TimingLimited: true, DecodeSamples: 8192, DecodeSkipped: 808}
	for _, lang := range []string{"en", "zh-CN"} {
		text := SampleCoverage(s, lang)
		if strings.Contains(text, "Partial history") || strings.Contains(text, "历史不完整") {
			t.Fatal("timing budget mislabeled the source", text)
		}
	}
}

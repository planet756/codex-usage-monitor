package presentation

import (
	"codex-usage-monitor/internal/metrics"
	"testing"
)

func TestCachePercentPrecisionMatchesStripAndDetails(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		want    string
	}{
		{97.30914061460719, "97.31%"}, {74, "74.00%"}, {0, "0.00%"}, {100, "100.00%"},
	} {
		x := metrics.Snapshot{CachePercent: &tc.percent}
		for _, lang := range []string{"en", "zh-CN"} {
			strip, rows := Strip(x, lang), DetailRows(x, lang, false)
			if strip[2].Value != tc.want || rows[1].Value != tc.want {
				t.Fatalf("%s cache precision differs: strip=%s detail=%s want=%s", lang, strip[2].Value, rows[1].Value, tc.want)
			}
		}
	}
	if Strip(metrics.Snapshot{}, "en")[2].Value != "-" {
		t.Fatal("unknown cache rate became zero")
	}
}

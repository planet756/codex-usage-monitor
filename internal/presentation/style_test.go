package presentation

import (
	"codex-usage-monitor/internal/metrics"
	"math"
	"testing"
)

func TestSessionRatePrecisionScopeAndLanguage(t *testing.T) {
	if got := Strip(metrics.Snapshot{}, "en"); got[0].Value != "-" {
		t.Fatal("missing samples fabricated a rate")
	}
	rate := 84.567
	s := metrics.Snapshot{TPS: &rate, TPSApproximate: true}
	for _, state := range []string{"waiting", "generating", "completed", "idle"} {
		s.Status = state
		if got := Strip(s, "en"); got[0].Value != "84.6" || got[0].Unit != "tok/s" {
			t.Fatalf("lifecycle changed cumulative speed: %+v", got)
		}
	}
	if got := DetailRows(s, "en", false)[8]; got.Value != "≈84.6" || got.Unit != "tok/s" {
		t.Fatalf("detail speed lost its approximation marker: %+v", got)
	}
	if got := Strip(s, "zh-CN"); got[2].Unit != "缓存命中率" {
		t.Fatal("language change did not apply")
	}
	rate = 210.2
	if got := Strip(s, "en"); got[0].Value != "210" {
		t.Fatal("large speed lost compact precision")
	}
}

func TestInvalidOrMissingRateUsesDashWithoutChangingActualZero(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		s := metrics.Snapshot{TPS: &value, TPSApproximate: true}
		if got := Strip(s, "en")[0]; got.Value != "-" || got.Unit != "tok/s" {
			t.Fatalf("invalid rate lost its placeholder or unit: %+v", got)
		}
		if got := DetailRows(s, "en", false)[8].Value; got != "-" {
			t.Fatalf("invalid detail rate displayed as a value: %q", got)
		}
	}
	s := metrics.Snapshot{TPS: new(0.0)}
	if got := Strip(s, "en")[0].Value; got != "0.0" {
		t.Fatalf("real zero rate became missing: %q", got)
	}
}

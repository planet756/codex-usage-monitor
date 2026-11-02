package presentation

import (
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/metrics"
	"fmt"
	"math"
)

type Palette struct{ Strip, Panel, Text, Muted, Border, Rule uint32 }

func Colors(dark bool) Palette {
	if dark {
		return Palette{0x292929, 0x252525, 0xF5F5F5, 0xC8C8C8, 0x3B3B3B, 0x363636}
	}
	return Palette{0xF6F6F6, 0xFFFFFF, 0x181818, 0x505050, 0xDEDEDE, 0xECECEC}
}

type Metric struct{ Value, Unit string }

const MissingValue = "-"

func displayRate(rate *float64, approximate bool) string {
	value := metrics.Rate(rate, approximate)
	if value == "—" {
		return MissingValue
	}
	return value
}

func Strip(s metrics.Snapshot, language string) [3]Metric {
	rate, total, cache := displayRate(s.TPS, false), MissingValue, MissingValue
	if s.HasUsage {
		total = metrics.Compact(s.Total)
	}
	if s.CachePercent != nil {
		cache = fmt.Sprintf("%.2f%%", *s.CachePercent)
	}
	return [3]Metric{{rate, i18n.Text(language, "tok/s")}, {total, i18n.Text(language, "tok")}, {cache, i18n.Text(language, "Cache hit")}}
}

func stripRateFallbacks(s metrics.Snapshot, raw string) [2]string {
	if s.TPS == nil || *s.TPS < 1000 || raw == MissingValue {
		return [2]string{raw, raw}
	}
	return [2]string{metrics.CompactRate(s.TPS, false, false), metrics.CompactRate(s.TPS, false, true)}
}

func Counts(s metrics.Snapshot, language string) string {
	return counts(s, language, metrics.Number)
}

func CompactCounts(s metrics.Snapshot, language string) string {
	return counts(s, language, compactCount(s))
}

func compactCount(s metrics.Snapshot) func(int64) string {
	if !s.CountsComplete {
		return metrics.CompactLowerBound
	}
	return metrics.Compact
}

func countValues(s metrics.Snapshot, format func(int64) string) (string, string) {
	turns, steps := MissingValue, MissingValue
	if s.HasCounts {
		turns, steps = format(s.Turns), format(s.Steps)
		if !s.CountsComplete {
			turns, steps = "≥"+turns, "≥"+steps
		}
	}
	return turns, steps
}

func counts(s metrics.Snapshot, language string, format func(int64) string) string {
	turns, steps := countValues(s, format)
	if language == "zh-CN" {
		return turns + " 轮 · " + steps + " 步"
	}
	turn, step := "turns", "steps"
	if s.HasCounts && s.Turns == 1 {
		turn = "turn"
	}
	if s.HasCounts && s.Steps == 1 {
		step = "step"
	}
	return turns + " " + turn + " · " + steps + " " + step
}

func SampleCoverage(s metrics.Snapshot, language string) string {
	if !validCoverage(s) {
		return i18n.Text(language, "Timing unavailable")
	}
	if s.DecodeSamples == 0 && s.DecodeSkipped == 0 {
		if !s.HistoryComplete {
			return i18n.Text(language, "Partial history · timing incomplete")
		}
		return i18n.Text(language, "Waiting for data")
	}
	value := i18n.Format(language, "TPS samples: %d/%d", s.DecodeSamples, s.DecodeSamples+s.DecodeSkipped)
	if s.DecodeSkipped > 0 {
		value += i18n.Format(language, " · Skipped: %d", s.DecodeSkipped)
	}
	if !s.HistoryComplete {
		value += " · " + i18n.Text(language, "Partial history")
	}
	if s.TimingLimited {
		value += " · " + i18n.Text(language, "Timing detail limit")
	}
	return value
}

func validCoverage(s metrics.Snapshot) bool {
	return s.DecodeSamples >= 0 && s.DecodeSkipped >= 0 && s.DecodeSamples <= math.MaxInt64-s.DecodeSkipped
}

type DetailRow struct{ Label, Value, Unit string }

// DetailRows shares the exact labels and formatters between native rendering
// and the development preview. The preview never supplies runtime statistics.
func DetailRows(s metrics.Snapshot, language string, loading bool) []DetailRow {
	tr := func(v string) string { return i18n.Text(language, v) }
	tokens := func(n int64) string {
		if !s.HasUsage {
			return MissingValue
		}
		return metrics.Number(n)
	}
	cache, uncached, cached := MissingValue, MissingValue, MissingValue
	if s.CachePercent != nil {
		cache = fmt.Sprintf("%.2f%%", *s.CachePercent)
	}
	if s.Uncached != nil {
		uncached, cached = tokens(*s.Uncached), tokens(s.Usage.Cached)
	}
	model, tool, ttft, rate := MissingValue, MissingValue, MissingValue, MissingValue
	if !loading && s.HasTiming {
		model, tool = metrics.Duration(s.ModelMS), metrics.Duration(s.ToolMS)
		if !s.HistoryComplete || !s.TimingComplete {
			model, tool = "≥ "+metrics.DurationLowerBound(s.ModelMS), "≥ "+metrics.DurationLowerBound(s.ToolMS)
		}
	}
	if !loading && s.AverageTTFT != nil {
		ttft = fmt.Sprintf("%.2f s", *s.AverageTTFT/1000)
	}
	if !loading && s.TPS != nil {
		rate = displayRate(s.TPS, s.TPSApproximate)
	}
	rows := []DetailRow{
		{"Total tokens", tokens(s.Total), "tok"},
		{"Cache hit", cache, ""}, {"Uncached input", uncached, "tok"},
		{"Cached input", cached, "tok"}, {"Output", tokens(s.Usage.Output), "tok"},
		{"LLM time", model, ""}, {"Tool time", tool, ""},
		{"Avg time to first token (TTFT)", ttft, ""}, {"Tokens per second (TPS)", rate, "tok/s"},
	}
	for i := range rows {
		rows[i].Label = tr(rows[i].Label)
		if rows[i].Value == MissingValue {
			rows[i].Unit = ""
		} else {
			rows[i].Unit = tr(rows[i].Unit)
		}
	}
	return rows
}

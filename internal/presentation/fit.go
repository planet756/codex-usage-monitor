package presentation

import (
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/metrics"
	"strings"
)

type StripChoices struct {
	Left  [][4]string
	Right [][3]string
}

type FittedStrip struct {
	Left  [4]string
	Right [3]string
}

func tightCounts(s metrics.Snapshot, language string, format func(int64) string) string {
	value := counts(s, language, format)
	if language == "zh-CN" {
		value = strings.ReplaceAll(value, " 轮", "轮")
		value = strings.ReplaceAll(value, " 步", "步")
	}
	return value
}

// Alternatives are ordered by readability. Every choice retains both counts,
// TPS with its unit, total tokens, cache label and two-decimal percentage. No choice changes
// the snapshot, sampling denominator or the meaning of a qualified count.
func StripAlternatives(s metrics.Snapshot, language string) StripChoices {
	p := Strip(s, language)
	count := CompactCounts(s, language)
	// Compact numbers and Chinese unit spacing while keeping separators legible.
	// Count labels stay explicit, including the scientific-notation fallback.
	tight := tightCounts(s, language, func(n int64) string { return strings.ReplaceAll(compactCount(s)(n), ".0", "") })
	whole := tightCounts(s, language, func(n int64) string { return metrics.CompactWhole(n, !s.CountsComplete) })
	scientific := tightCounts(s, language, func(n int64) string { return metrics.ScientificCount(n, !s.CountsComplete) })
	rates := stripRateFallbacks(s, p[0].Value)
	rate, lastRate := rates[0], rates[1]
	unit := " " + p[0].Unit
	total, lastTotal := p[1].Value, p[1].Value
	cacheLabel := p[2].Unit
	if language == "zh-CN" {
		cacheLabel = i18n.Text(language, "cache")
	}
	if s.HasUsage {
		total = metrics.CompactWhole(s.Total, false)
		lastTotal = metrics.ScientificCount(s.Total, false)
	}
	return StripChoices{
		Left: [][4]string{
			{Counts(s, language), " · ", p[0].Value, unit},
			{count, " · ", p[0].Value, unit},
			{tight, " · ", p[0].Value, unit},
			{tight, " · ", rate, unit},
			{whole, " · ", rate, unit},
			{scientific, " · ", lastRate, unit},
		},
		Right: [][3]string{
			{p[1].Value, " " + p[1].Unit + " · " + p[2].Unit + " ", p[2].Value},
			{p[1].Value, " " + p[1].Unit + " · " + cacheLabel + " ", p[2].Value},
			{total, " " + p[1].Unit + " · " + cacheLabel + " ", p[2].Value},
			{lastTotal, " " + p[1].Unit + " · " + cacheLabel + " ", p[2].Value},
		},
	}
}

func fits(parts []string, available int, measure func(string) int) bool {
	if available < 0 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			continue
		}
		n := measure(part)
		if n < 0 || n > available {
			return false
		}
		available -= n
	}
	return true
}

// SelectStrip measures the same separate layouts the native renderer paints. A failed
// final choice is explicit; callers must not draw and silently clip it.
func SelectStrip(options StripChoices, left, right int, measure func(string) int) (FittedStrip, bool) {
	var result FittedStrip
	l, r := false, false
	for _, option := range options.Left {
		if fits(option[:], left, measure) {
			result.Left, l = option, true
			break
		}
	}
	for _, option := range options.Right {
		if fits(option[:], right, measure) {
			result.Right, r = option, true
			break
		}
	}
	return result, l && r
}

func CountAlternatives(s metrics.Snapshot, language string) []string {
	return []string{Counts(s, language), CompactCounts(s, language),
		tightCounts(s, language, compactCount(s)),
		tightCounts(s, language, func(n int64) string { return metrics.CompactWhole(n, !s.CountsComplete) }),
		tightCounts(s, language, func(n int64) string { return metrics.ScientificCount(n, !s.CountsComplete) })}
}

// FitStripWidth finds the smallest compact width that keeps full count labels,
// tok/s and the full English cache label. Normal token precision takes priority over
// an earlier width bucket; coarser totals are reserved for actual space limits.
// Eight-DIP steps avoid resizing for every single-pixel rate change;
// inset and divider spacing never grow with the window. Every probe uses the
// same final fit check as drawing, including physical-pixel rounding at DPI.
func FitStripWidth(options StripChoices, dpi int, measure func(string) int) (int, bool) {
	if dpi <= 0 {
		dpi = 96
	}
	scale := func(n int) int { return (n*dpi + 48) / 96 }
	preferred := options
	for i, right := range options.Right {
		if right[0] != options.Right[0][0] {
			preferred.Right = options.Right[:i]
			break
		}
	}
	for _, choices := range [2]StripChoices{preferred, options} {
		for dip := layout.StripWidth; dip <= layout.StripMaxWidth; dip += layout.StripWidthStep {
			width := scale(dip)
			c := layout.SplitStrip(width, scale(26), dpi)
			if _, ok := SelectStrip(choices, c.Left.W-scale(1), c.Right.W-scale(1), measure); ok {
				return width, true
			}
		}
	}
	return 0, false
}

func CoverageAlternatives(s metrics.Snapshot, language string) []string {
	full := SampleCoverage(s, language)
	if !validCoverage(s) || s.DecodeSamples == 0 && s.DecodeSkipped == 0 {
		return []string{full}
	}
	result := []string{full}
	for _, format := range []func(int64) string{metrics.Compact, func(n int64) string { return metrics.ScientificCount(n, false) }} {
		value := i18n.Format(language, "Samples: %s/%s", format(s.DecodeSamples), format(s.DecodeSamples+s.DecodeSkipped))
		if s.DecodeSkipped > 0 {
			value += i18n.Format(language, " · Skip %s", format(s.DecodeSkipped))
		}
		if !s.HistoryComplete {
			value += " · " + i18n.Text(language, "Partial history")
		}
		result = append(result, value)
	}
	return result
}

func SelectText(options []string, available int, measure func(string) int) (string, bool) {
	for _, text := range options {
		if fits([]string{text}, available, measure) {
			return text, true
		}
	}
	return "", false
}

// Detail values remain exact. Only redundant units and labels may shorten.
func DetailAlternatives(row DetailRow, language string) []DetailRow {
	short := row.Label
	for _, pair := range [][2]string{{"Total tokens", "Total"}, {"Uncached input", "Uncached"}, {"Cached input", "Cached"},
		{"Avg time to first token (TTFT)", "Avg TTFT"}, {"Tokens per second (TPS)", "TPS"},
		{"LLM time", "LLM"}, {"Tool time", "Tools"}, {"Cache hit", "Cache"}} {
		if row.Label == i18n.Text(language, pair[0]) {
			short = i18n.Text(language, pair[1])
			break
		}
	}
	return []DetailRow{row, {row.Label, row.Value, ""}, {short, row.Value, row.Unit}, {short, row.Value, ""}}
}

package presentation

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/metrics"
)

// Conservative 12 DIP glyph widths model native span measurements independently
// of the candidate-selection code. Browser and Windows-font checks cover the
// actual advances as well; these tests exercise all qualifier and limit paths.
func testWidth(value string) int {
	n := 0
	for _, r := range value {
		switch {
		case unicode.Is(unicode.Han, r):
			n += 12
		case r == ' ' || r == '·' || r == '.' || r == ',':
			n += 3
		case r == '≥' || r == '≈':
			n += 8
		case r == 'M' || r == 'W' || r == '—':
			n += 11
		case r == 'e':
			n += 6
		case r == 't':
			n += 5
		case r == 'o':
			n += 8
		case r == 'k' || r == 's' || r == '/':
			n += 6
		default:
			n += 7
		}
	}
	return n
}

func assertCountLabels(t *testing.T, value, language string, turns, steps int64) {
	t.Helper()
	if language == "zh-CN" {
		if !strings.Contains(value, "轮") || !strings.Contains(value, "步") {
			t.Fatalf("count labels shortened: %q", value)
		}
	} else {
		turn, step := "turns", "steps"
		if turns == 1 {
			turn = "turn"
		}
		if steps == 1 {
			step = "step"
		}
		if !strings.Contains(value, turn) || !strings.Contains(value, step) {
			t.Fatalf("count labels shortened: %q", value)
		}
	}
}

func TestEveryCountFallbackRetainsLabelsAndWidthNeverAddsInsets(t *testing.T) {
	rate := math.MaxFloat64
	for _, lang := range []string{"en", "zh-CN"} {
		s := metrics.Snapshot{HasCounts: true, Turns: math.MaxInt64, Steps: math.MaxInt64, TPS: &rate, TPSApproximate: true}
		for _, option := range StripAlternatives(s, lang).Left {
			assertCountLabels(t, option[0], lang, s.Turns, s.Steps)
			if strings.TrimSpace(option[3]) != "tok/s" || strings.Contains(option[2], "≈") {
				t.Fatal("strip rate policy changed")
			}
		}
		for _, option := range CountAlternatives(s, lang) {
			assertCountLabels(t, option, lang, s.Turns, s.Steps)
		}
		for _, option := range StripAlternatives(s, lang).Right {
			label := "Cache hit "
			if lang == "zh-CN" {
				label = "缓存"
			}
			if !strings.Contains(option[1], label) || strings.Contains(option[1], "  ") {
				t.Fatalf("cache label lost or separator spacing expanded: %q", option)
			}
		}
		if _, ok := FitStripWidth(StripAlternatives(s, lang), 96, testWidth); !ok {
			t.Fatal("largest finite rate cannot fit full labels")
		}
	}
}

func TestStripFitsChineseEnglishLargeAndQualifiedCounts(t *testing.T) {
	for _, complete := range []bool{true, false} {
		for _, n := range []int64{0, 1999, 123456, 1999999, 999999999, 1000000000000, math.MaxInt64} {
			for _, rate := range []float64{31.6, 208, 999999, 9.223372036854776e21} {
				for _, lang := range []string{"en", "zh-CN"} {
					originalRate := rate
					s := metrics.Snapshot{HasCounts: true, CountsComplete: complete, Turns: n, Steps: n, TPS: &rate, TPSApproximate: true}
					s = s.WithUsage(metrics.Usage{Input: n, Cached: n}, true)
					before := s
					for _, dpi := range []int{96, 120, 144, 168, 192} {
						scale := func(n int) int { return (n*dpi + 48) / 96 }
						measure := func(value string) int {
							if value == "" {
								return 0
							}
							return scale(testWidth(value)) + 2
						}
						options := StripAlternatives(s, lang)
						w, ok := FitStripWidth(options, dpi, measure)
						if !ok || w < scale(layout.StripWidth) || w > scale(layout.StripMaxWidth) {
							t.Fatalf("no measured width lang=%s count=%d rate=%g dpi=%d", lang, n, rate, dpi)
						}
						columns := layout.SplitStrip(w, scale(26), dpi)
						chosen, ok := SelectStrip(options, columns.Left.W-scale(1), columns.Right.W-scale(1), measure)
						if !ok {
							t.Fatalf("no fitting display lang=%s complete=%v n=%d rate=%g width=%d", lang, complete, n, rate, w)
						}
						if !fits(chosen.Left[:], columns.Left.W-scale(1), measure) || !fits(chosen.Right[:], columns.Right.W-scale(1), measure) {
							t.Fatal("overflowing final choice returned")
						}
						assertCountLabels(t, chosen.Left[0], lang, s.Turns, s.Steps)
						label := "Cache hit "
						if lang == "zh-CN" {
							label = "缓存"
						}
						if !strings.Contains(chosen.Right[1], label) {
							t.Fatalf("full cache label removed at dpi=%d: %q", dpi, chosen.Right)
						}
						// No earlier bucket fits the chosen token precision. Extra
						// width may preserve precision but must not add padding.
						if previous := w - scale(layout.StripWidthStep); previous >= scale(layout.StripWidth) {
							c := layout.SplitStrip(previous, scale(26), dpi)
							atPrecision := options
							atPrecision.Right = nil
							for _, right := range options.Right {
								if right[0] == chosen.Right[0] {
									atPrecision.Right = append(atPrecision.Right, right)
								}
							}
							if _, fits := SelectStrip(atPrecision, c.Left.W-scale(1), c.Right.W-scale(1), measure); fits {
								t.Fatal("width grew beyond the first fitting bucket")
							}
						}
						if !complete && strings.Count(chosen.Left[0], "≥") != 2 {
							t.Fatal("a lower-bound qualifier was removed")
						}
						if strings.Contains(strings.Join(chosen.Left[:], ""), "≈") || strings.TrimSpace(chosen.Left[3]) != "tok/s" || chosen.Right[2] != "100.00%" && n > 0 {
							t.Fatal("strip speed unit, marker policy or two-decimal cache percentage lost")
						}
					}
					if !reflect.DeepEqual(s, before) || rate != originalRate {
						t.Fatal("display fitting mutated statistics")
					}
				}
			}
		}
	}
}

func TestStripWidthKeepsNormalTokenPrecisionBeforeCoarserTotals(t *testing.T) {
	rate := 208.0
	s := metrics.Snapshot{HasCounts: true, CountsComplete: true, Turns: 1, Steps: 5, TPS: &rate}
	s = s.WithUsage(metrics.Usage{Input: 98481, Cached: 72704, Output: 5629}, true)
	for _, language := range []string{"en", "zh-CN"} {
		options := StripAlternatives(s, language)
		w, ok := FitStripWidth(options, 96, testWidth)
		if !ok {
			t.Fatal("normal usage precision cannot fit")
		}
		c := layout.SplitStrip(w, 26, 96)
		chosen, ok := SelectStrip(options, c.Left.W-1, c.Right.W-1, testWidth)
		if !ok || chosen.Right[0] != "104.1K" || chosen.Right[2] != "73.83%" {
			t.Fatalf("usage simplified before available width was used: %q", chosen.Right)
		}
		// A real narrow-caption constraint may still choose a coarser number,
		// but it must retain the cache label and the exact displayed percentage.
		last := options.Right[len(options.Right)-1]
		narrowRight := 0
		for _, part := range last {
			narrowRight += testWidth(part)
		}
		chosen, ok = SelectStrip(options, c.Left.W-1, narrowRight, testWidth)
		label := "Cache hit "
		if language == "zh-CN" {
			label = "缓存"
		}
		if !ok || chosen.Right[2] != "73.83%" || !strings.Contains(chosen.Right[1], label) {
			t.Fatal("narrow caption removed a required field")
		}
	}
}

func TestFinalWidthFailureIsExplicitAndMeasurementsCannotOverflow(t *testing.T) {
	options := StripAlternatives(metrics.Snapshot{}, "en")
	for _, measure := range []func(string) int{func(string) int { return 10000 }, func(string) int { return -1 }, func(string) int { return math.MaxInt }} {
		if _, ok := FitStripWidth(options, 96, measure); ok {
			t.Fatal("invalid measurement produced a width")
		}
		if _, ok := SelectStrip(options, 142, 128, measure); ok {
			t.Fatal("unmeasurable or oversized spans treated as fitting")
		}
	}
	if _, ok := SelectStrip(options, -1, 128, testWidth); ok {
		t.Fatal("negative width accepted")
	}
}

func TestUnknownCountsStayUnknownAndDetailsRetainExactValues(t *testing.T) {
	options := StripAlternatives(metrics.Snapshot{}, "en")
	w, ok := FitStripWidth(options, 96, testWidth)
	if !ok {
		t.Fatal("unknown values cannot fit")
	}
	c := layout.SplitStrip(w, 26, 96)
	chosen, ok := SelectStrip(options, c.Left.W-1, c.Right.W-1, testWidth)
	if !ok || !strings.Contains(chosen.Left[0], "-") || chosen.Left[2] != "-" || chosen.Right[0] != "-" || chosen.Right[2] != "-" {
		t.Fatal("unknown statistics fabricated zeros")
	}
	for _, lang := range []string{"en", "zh-CN"} {
		s := metrics.Snapshot{}.WithUsage(metrics.Usage{Input: math.MaxInt64, Cached: math.MaxInt64}, true)
		for _, row := range DetailRows(s, lang, false) {
			for _, option := range DetailAlternatives(row, lang) {
				if option.Value != row.Value {
					t.Fatal("detail value rounded or changed")
				}
			}
		}
	}
}

func TestInvalidSampleSumCannotWrapToAValidCoverage(t *testing.T) {
	s := metrics.Snapshot{DecodeSamples: math.MaxInt64, DecodeSkipped: 1}
	if got := SampleCoverage(s, "en"); got != "Timing unavailable" {
		t.Fatalf("overflowed sample denominator: %s", got)
	}
}

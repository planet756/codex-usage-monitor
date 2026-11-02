package metrics

import (
	"math"
	"strconv"
	"strings"
)

var displayUnits = [...]struct {
	divisor int64
	suffix  string
}{
	{1_000, "K"}, {1_000_000, "M"}, {1_000_000_000, "B"}, {1_000_000_000_000, "T"},
}

// Integer division keeps lower-bound formatting exact even above 2^53. Carry
// rounded complete values into the next unit, never a lower bound such as ≥999.9K.
func compactNumber(n int64, lower, whole bool) string {
	if n < 1_000 {
		return strconv.FormatInt(n, 10)
	}
	u := 0
	for u+1 < len(displayUnits) && n >= displayUnits[u+1].divisor {
		u++
	}
	for {
		unit := displayUnits[u]
		precision := int64(10)
		if whole {
			precision = 1
		}
		quantum := unit.divisor / precision
		value := n / quantum
		if !lower && n%quantum >= quantum/2 {
			value++
		}
		if value >= 1_000*precision && u+1 < len(displayUnits) {
			u++
			continue
		}
		if whole {
			return strconv.FormatInt(value, 10) + unit.suffix
		}
		return strconv.FormatInt(value/10, 10) + "." + strconv.FormatInt(value%10, 10) + unit.suffix
	}
}

func CompactWhole(n int64, lower bool) string { return compactNumber(n, lower, true) }

// ScientificCount is the last display-only fallback. Qualified counts are
// truncated to one significant digit so the ≥ assertion always stays true.
func ScientificCount(n int64, lower bool) string {
	if n < 1_000 {
		return strconv.FormatInt(n, 10)
	}
	scale, exponent := int64(1), 0
	for scale <= n/10 {
		scale *= 10
		exponent++
	}
	leading := n / scale
	if !lower && n%scale >= scale/2 {
		leading++
	}
	if leading == 10 {
		leading = 1
		exponent++
	}
	return strconv.FormatInt(leading, 10) + "e" + strconv.Itoa(exponent)
}

func CompactRate(rate *float64, approximate, scientific bool) string {
	if rate == nil || math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 {
		return "—"
	}
	value := Rate(rate, false)
	if scientific && *rate >= 1_000 {
		value = shortExponent(strconv.FormatFloat(*rate, 'e', 0, 64))
	} else if *rate >= 1_000 {
		u := 0
		for u+1 < len(displayUnits) && *rate >= float64(displayUnits[u+1].divisor) {
			u++
		}
		for {
			rounded := math.Round(*rate/float64(displayUnits[u].divisor)*10) / 10
			if rounded >= 1_000 && u+1 < len(displayUnits) {
				u++
				continue
			}
			value = strconv.FormatFloat(rounded, 'f', 1, 64)
			value = strings.TrimSuffix(value, ".0") + displayUnits[u].suffix
			break
		}
	}
	if approximate {
		value = "≈" + value
	}
	return value
}

func shortExponent(value string) string {
	parts := strings.SplitN(value, "e", 2)
	if len(parts) != 2 {
		return value
	}
	exponent, err := strconv.Atoi(parts[1])
	if err != nil {
		return value
	}
	return parts[0] + "e" + strconv.Itoa(exponent)
}

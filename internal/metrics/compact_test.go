package metrics

import (
	"math"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestCompactDecimalUnitsCarryAndInt64Precision(t *testing.T) {
	for _, tc := range []struct {
		n             int64
		normal, lower string
	}{
		{999, "999", "999"}, {1000, "1.0K", "1.0K"}, {1999, "2.0K", "1.9K"},
		{999949, "999.9K", "999.9K"}, {999950, "1.0M", "999.9K"},
		{999950000, "1.0B", "999.9M"}, {999950000000, "1.0T", "999.9B"},
		{1234567890, "1.2B", "1.2B"}, {1234567890123, "1.2T", "1.2T"},
		{math.MaxInt64, "9223372.0T", "9223372.0T"},
	} {
		if got := Compact(tc.n); got != tc.normal {
			t.Fatalf("Compact(%d)=%s want %s", tc.n, got, tc.normal)
		}
		if got := CompactLowerBound(tc.n); got != tc.lower {
			t.Fatalf("lower(%d)=%s want %s", tc.n, got, tc.lower)
		}
	}
}

func parseDisplay(t *testing.T, value string) *big.Rat {
	t.Helper()
	if pieces := strings.SplitN(value, "e", 2); len(pieces) == 2 {
		exp, err := strconv.Atoi(pieces[1])
		if err != nil {
			t.Fatal(err)
		}
		lead, ok := new(big.Int).SetString(pieces[0], 10)
		if !ok {
			t.Fatal(value)
		}
		return new(big.Rat).SetInt(new(big.Int).Mul(lead, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)))
	}
	factor := int64(1)
	for suffix, mult := range map[string]int64{"K": 1000, "M": 1000000, "B": 1000000000, "T": 1000000000000} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix)
			factor = mult
			break
		}
	}
	n, ok := new(big.Rat).SetString(value)
	if !ok {
		t.Fatal(value)
	}
	return n.Mul(n, new(big.Rat).SetInt64(factor))
}

func TestQualifiedFormatsNeverExceedObservedCount(t *testing.T) {
	values := []int64{0, 999, 1000, 1999, 999999, 999999999, 999999999999, 1<<53 - 1, 1 << 53, 1<<53 + 1, math.MaxInt64}
	r := rand.New(rand.NewSource(42))
	for range 2000 {
		values = append(values, r.Int63())
	}
	for _, n := range values {
		for _, format := range []func(int64) string{CompactLowerBound, func(n int64) string { return CompactWhole(n, true) }, func(n int64) string { return ScientificCount(n, true) }} {
			text := format(n)
			if parseDisplay(t, text).Cmp(new(big.Rat).SetInt64(n)) > 0 {
				t.Fatalf("qualified value exceeds actual %d: %s", n, text)
			}
		}
	}
}

func TestCompactRatesKeepApproximationAndRejectInvalidValues(t *testing.T) {
	for _, rate := range []float64{0, 31.6, 999, 1000, 999950, 1e9, 1e12, 9.223372036854776e21} {
		original := rate
		for _, scientific := range []bool{false, true} {
			if got := CompactRate(&rate, true, scientific); !strings.HasPrefix(got, "≈") {
				t.Fatalf("approximation lost: %s", got)
			}
		}
		if rate != original {
			t.Fatal("formatting changed rate")
		}
	}
	for _, rate := range []float64{-1, math.NaN(), math.Inf(1)} {
		if Rate(&rate, true) != "—" || CompactRate(&rate, true, true) != "—" {
			t.Fatal("invalid rate displayed as valid")
		}
	}
}

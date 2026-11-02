package metrics

import (
	"fmt"
	"math"
	"time"
)

// Desktop exposes item creation times, not the first nonempty provider token
// timestamp. These bounds produce an explicitly approximate decode speed.
// A usage record closes one response before its tools execute.
type decodeBoundary struct {
	Start, End int64
	Invalid    bool
}

func (b *decodeBoundary) observe(start, end int64) {
	if start <= 0 || end < start || end-start > maxDurationMS {
		b.Invalid = true
		return
	}
	if b.Start == 0 || start < b.Start {
		b.Start = start
	}
	b.End = max(b.End, end)
}

func (s *State) finishDecode(t *Turn, usage *Usage, ended time.Time) {
	b := t.PendingDecode
	unknown := t.BoundaryUnknown
	t.PendingDecode = decodeBoundary{}
	validEnd := !ended.IsZero() && ended.UnixMilli() >= b.End
	if validEnd {
		t.LastResponseMS = max(t.LastResponseMS, ended.UnixMilli())
		t.BoundaryUnknown = false
	} else if usage != nil || b.Start > 0 {
		t.BoundaryUnknown = true
	}
	// Never pair tokens with a partial, missing, or mismatched timing window.
	// Whole-turn TTFT cannot establish a per-response first-token timestamp.
	if unknown || !t.Started || t.Incomplete || b.Invalid || usage == nil || !usage.Valid() ||
		b.Start <= 0 || ended.IsZero() || ended.UnixMilli() < b.End {
		s.DecodeSkipped++
		s.TimingComplete = false
		return
	}
	ms := ended.UnixMilli() - b.Start
	if ms <= 0 || ms > maxDurationMS ||
		s.DecodeTokens > math.MaxInt64-usage.Output || s.DecodeMS > math.MaxInt64-ms {
		s.DecodeSkipped++
		s.TimingComplete = false
		return
	}
	s.DecodeTokens += usage.Output // Includes reasoning and generated tool arguments.
	s.DecodeMS += ms
	s.DecodeSamples++
}

func Rate(rate *float64, approximate bool) string {
	if rate == nil || math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0 {
		return "—"
	}
	precision := 1
	if *rate >= 100 {
		precision = 0
	}
	value := fmt.Sprintf("%.*f", precision, *rate)
	if approximate {
		value = "≈" + value
	}
	return value
}

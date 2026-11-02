package metrics

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"time"
)

// SemanticHash binds every field used by accounting, including absence flags.
// Fixed-size field digests avoid re-encoding a large record and allocating a
// second JSON object while comparing two representations of the same history.
func (r Record) SemanticHash() [32]byte {
	var storage [512]byte
	b := storage[:0]
	number := func(n int64) { b = binary.LittleEndian.AppendUint64(b, uint64(n)) }
	text := func(s string) { sum := sha256.Sum256([]byte(s)); b = append(b, sum[:]...) }
	optional := func(n *int64) {
		if n == nil {
			b = append(b, 0)
		} else {
			b = append(b, 1)
			number(*n)
		}
	}
	usage := func(u *Usage) {
		if u == nil {
			b = append(b, 0)
			return
		}
		b = append(b, 1)
		number(u.Input)
		number(u.Cached)
		number(u.Output)
		number(u.Reasoning)
		if u.cacheKnown {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
		if u.Valid() {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	p := r.Payload
	text(r.Type)
	optional(r.Ordinal)
	text(p.Type)
	text(p.ID)
	text(p.TurnID)
	text(p.ThreadID)
	text(p.ResponseID)
	text(p.Item.Type)
	number(p.Start)
	number(p.End)
	optional(p.TTFT)
	usage(p.Usage)
	usage(p.ThreadUsage)
	if r.Type == "token_usage_record" {
		stamp, e := time.Parse(time.RFC3339Nano, r.Timestamp)
		if e == nil {
			b = append(b, 1)
			number(stamp.UnixMilli())
		} else {
			b = append(b, 0)
		}
	}
	if r.Type == "session_meta" {
		var ref *struct {
			ID      string `json:"thread_id"`
			Ordinal *int64 `json:"end_ordinal_exclusive"`
			Bytes   *int64 `json:"end_byte_offset"`
		}
		if len(p.HistoryBase) > 0 && json.Unmarshal(p.HistoryBase, &ref) != nil {
			b = append(b, 2)
			sum := sha256.Sum256(p.HistoryBase)
			b = append(b, sum[:]...)
		} else if ref == nil {
			b = append(b, 0)
		} else {
			b = append(b, 1)
			text(ref.ID)
			optional(ref.Ordinal)
			optional(ref.Bytes)
		}
	}
	return sha256.Sum256(b)
}

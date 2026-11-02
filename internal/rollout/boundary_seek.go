package rollout

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// Byte offsets are search hints only. An exact ordinal anchors the cut; the
// reader validates the complete sequence and source proofs before publication.
// This avoids parsing an inherited prefix twice on its first cold load.
func anchoredBoundary(ctx context.Context, f io.ReaderAt, size int64, s Segment) (int64, error) {
	target := s.EndOrdinal - 1
	pos := min(s.Bytes-1, size-1)
	for attempts := 0; attempts < 64 && pos >= 0 && pos < size; attempts++ {
		start, end, ordinal, err := recordAt(ctx, f, size, pos)
		if err != nil {
			break
		}
		if ordinal == target {
			return end, nil
		}
		if ordinal < target {
			pos = end
		} else {
			pos = start - 1
		}
	}
	lo, hi := int64(0), size
	for attempts := 0; lo < hi && attempts < 64; attempts++ {
		start, end, ordinal, err := recordAt(ctx, f, size, lo+(hi-lo)/2)
		if err != nil {
			break
		}
		if ordinal == target {
			return end, nil
		}
		if ordinal < target {
			if end <= lo {
				break
			}
			lo = end
		} else {
			if start >= hi {
				break
			}
			hi = start
		}
	}
	// Corrupt discarded tails must not prevent a valid earlier prefix from
	// being found; the bounded sequential fallback stops at the referenced cut.
	return ordinalBoundary(ctx, f, size, s)
}

func recordAt(ctx context.Context, f io.ReaderAt, size, pos int64) (start, end, ordinal int64, err error) {
	if pos < 0 || pos >= size {
		return 0, 0, 0, ErrHistoryBoundary
	}
	start = -1
	for width := int64(4096); ; width *= 2 {
		if e := ctx.Err(); e != nil {
			return 0, 0, 0, e
		}
		width = min(width, int64(MaxRecordBytes+1))
		at := max(int64(0), pos-width)
		data := make([]byte, pos-at)
		if len(data) > 0 {
			if _, e := f.ReadAt(data, at); e != nil {
				return 0, 0, 0, e
			}
		}
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			start = at + int64(i) + 1
			break
		}
		if at == 0 {
			start = 0
			break
		}
		if width == MaxRecordBytes+1 {
			return 0, 0, 0, ErrHistoryBoundary
		}
	}
	br := bufio.NewReaderSize(io.NewSectionReader(f, start, size-start), 4096)
	var line []byte
	for {
		if e := ctx.Err(); e != nil {
			return 0, 0, 0, e
		}
		part, e := br.ReadSlice('\n')
		if len(line)+len(part) > MaxRecordBytes {
			return 0, 0, 0, ErrHistoryBoundary
		}
		line = appendRecord(line, part)
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil {
			return 0, 0, 0, ErrHistoryBoundary
		}
		break
	}
	var r struct {
		Ordinal *int64 `json:"ordinal"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(line, &r) != nil || r.Ordinal == nil || *r.Ordinal < 0 || r.Type == "" {
		return 0, 0, 0, ErrHistoryBoundary
	}
	return start, start + int64(len(line)), *r.Ordinal, nil
}

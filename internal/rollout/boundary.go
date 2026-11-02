package rollout

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// BoundaryCache belongs to one selected chat's reader. It retains only bounded
// file metadata and verified offsets, never source text or open file handles.
type BoundaryCache struct {
	entries         map[boundaryKey]boundaryEntry
	archives        map[string]archiveInfo
	archiveFailures map[string]sourceStamp
	equivalences    map[string]equivalence
	dirty           bool
}

type boundaryKey struct {
	path                 string
	header               [32]byte
	start, end, declared int64
}

type boundaryEntry struct {
	stamp sourceStamp
	bytes int64
}

func (c *BoundaryCache) resolve(ctx context.Context, s Segment) (Segment, error) {
	if err := ctx.Err(); err != nil {
		return s, err
	}
	f, err := os.Open(s.Path)
	if err != nil {
		return s, ErrHistoryMissing
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return s, ErrLogUnreadable
	}
	stamp := stampFile(f, info)
	var data io.ReaderAt = f
	size := info.Size()
	if s.Archive != nil {
		if s.Archive.Stamp != stamp {
			return s, ErrHistoryChanged
		}
		z := &archiveFile{File: f, ctx: ctx, archive: *s.Archive}
		defer z.release()
		data = z
		size = s.Archive.Size
	}
	key := boundaryKey{s.Path, s.Header, s.StartOrdinal, s.EndOrdinal, s.Bytes}
	if c != nil {
		if old, ok := c.entries[key]; ok && old.stamp.unchanged(stamp) {
			s.Bytes, s.Recovered = old.bytes, old.bytes != s.Bytes
			return s, nil
		}
	}
	if err = checkBoundary(ctx, data, size, s); err == nil {
		return s, nil
	}
	if !errors.Is(err, ErrHistoryBoundary) {
		return s, err
	}
	if s.StartOrdinal < 0 || s.EndOrdinal <= s.StartOrdinal || s.Bytes <= 0 {
		return s, ErrHistoryBoundary
	}
	// Two valid but different cuts are conflicting evidence. Rebuilding only
	// applies to an obsolete byte location, never to an ordinal disagreement at
	// an otherwise valid record boundary.
	if s.Bytes <= size {
		if line, e := lineEndingAt(data, s.Bytes); e == nil {
			var record struct {
				Ordinal *int64 `json:"ordinal"`
			}
			if json.Unmarshal(line, &record) == nil && record.Ordinal != nil {
				return s, ErrHistoryBoundary
			}
		}
	}
	var n int64
	if s.Archive != nil {
		n, err = ordinalBoundary(ctx, data, size, s)
	} else {
		n, err = anchoredBoundary(ctx, data, size, s)
	}
	if err != nil {
		return s, err
	}
	current, err := stampPath(s.Path)
	if err != nil || stamp != current {
		return s, ErrHistoryChanged
	}
	s.Bytes, s.Recovered = n, true
	if c != nil {
		if c.entries == nil || len(c.entries) >= maxHistoryCandidates {
			c.entries = make(map[boundaryKey]boundaryEntry)
		}
		c.entries[key] = boundaryEntry{current, n}
		c.dirty = true
	}
	return s, nil
}

// Locate the exact referenced end ordinal in increasing record order. Interior
// gaps are reported during replay; the boundary record itself must still exist.
func ordinalBoundary(ctx context.Context, f io.ReaderAt, size int64, s Segment) (int64, error) {
	br := bufio.NewReaderSize(io.NewSectionReader(f, 0, size), 64<<10)
	var pending []byte
	var offset int64
	expected := s.StartOrdinal
	for expected < s.EndOrdinal {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		part, err := br.ReadSlice('\n')
		offset += int64(len(part))
		if len(pending)+len(part) > MaxRecordBytes {
			return 0, ErrHistoryBoundary
		}
		line := part
		if len(pending) > 0 || errors.Is(err, bufio.ErrBufferFull) {
			pending = appendRecord(pending, part)
			line = pending
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, ErrHistoryBoundary
			}
			return 0, ErrLogUnreadable
		}
		var record struct {
			Ordinal *int64 `json:"ordinal"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type == "" || record.Ordinal == nil || *record.Ordinal >= s.EndOrdinal {
			return 0, ErrHistoryBoundary
		}
		if expected == s.StartOrdinal && (record.Type != "session_meta" || *record.Ordinal != s.StartOrdinal || sha256.Sum256(line) != s.Header) {
			return 0, ErrHistoryChanged
		}
		expected, err = advanceOrdinal(expected, *record.Ordinal)
		if err != nil {
			return 0, ErrHistoryBoundary
		}
		pending = pending[:0]
	}
	return offset, ctx.Err()
}

package rollout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
)

type equivalence struct {
	Plain, Compressed sourceStamp
	End               int64
}

func sameMetadata(a, b logMetadata) bool { return reflect.DeepEqual(a, b) }

// Cross-directory recovery copies are accepted only after their selected
// accounting prefix has been proved equivalent. Sibling precedence belongs to
// Codex's representation rules; unrelated same-format copies stay ambiguous.
func (c *BoundaryCache) equivalent(ctx context.Context, plain, compressed logNode, s Segment) error {
	a, err := stampPath(plain.path)
	if err != nil {
		return err
	}
	b, err := stampPath(compressed.path)
	if err != nil {
		return err
	}
	keyBytes, _ := json.Marshal([]any{plain.path, compressed.path, s.StartOrdinal, s.EndOrdinal, s.Bytes})
	k := sha256.Sum256(keyBytes)
	key := hex.EncodeToString(k[:])
	if old, ok := c.equivalences[key]; ok && old.Plain.unchanged(a) && old.Compressed.unchanged(b) && old.End == s.EndOrdinal {
		return nil
	}
	f, err := os.Open(plain.path)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := openArchive(ctx, compressed.path, compressed.archive)
	if err != nil {
		return err
	}
	defer z.Close()
	end := s.EndOrdinal
	left, err := accountingDigest(ctx, f, a.Size, end)
	if err != nil {
		return err
	}
	right, err := accountingDigest(ctx, z, z.archive.Size, end)
	if err != nil {
		return err
	}
	if left != right {
		return ErrLogAmbiguous
	}
	currentA, ea := stampPath(plain.path)
	currentB, eb := stampPath(compressed.path)
	if ea != nil || eb != nil || currentA != a || currentB != b {
		return ErrHistoryChanged
	}
	if c.equivalences == nil || len(c.equivalences) >= maxHistoryCandidates {
		c.equivalences = make(map[string]equivalence)
	}
	c.equivalences[key] = equivalence{a, b, end}
	c.dirty = true
	return nil
}

func accountingDigest(ctx context.Context, r io.ReaderAt, size, end int64) ([32]byte, error) {
	h := sha256.New()
	var expected int64 = -1
	reached := end < 0
	err := scanPrefix(ctx, r, size, func(line []byte) error {
		if len(bytes.TrimSpace(line)) == 0 {
			return nil
		}
		record, err := semanticRecord(line)
		if err != nil {
			return err
		}
		if record.Ordinal == nil {
			return ErrLogAmbiguous
		}
		if expected < 0 {
			expected = *record.Ordinal
		}
		expected, err = advanceOrdinal(expected, *record.Ordinal)
		if err != nil {
			return err
		}
		if end >= 0 && *record.Ordinal >= end {
			return errDigestDone
		}
		if end >= 0 && expected == end {
			reached = true
		}
		// Unknown message bodies have no accounting semantics. All fields used
		// by the aggregator, including missing/invalid usage flags, remain bound.
		return writeSemantic(h, record)
	})
	if err == errDigestDone {
		err = nil
	}
	if err == nil && !reached {
		err = ErrHistoryBoundary
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum, err
}

var errDigestDone = io.EOF

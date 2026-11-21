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

// Duplicate archives can use different Zstandard encodings for the same log.
// Reuse only a proof bound to both file identities.
func (c *BoundaryCache) verifyArchiveCopy(ctx context.Context, first, second logNode) error {
	if second.path < first.path {
		first, second = second, first
	}
	a, b := first.archive.Stamp, second.archive.Stamp
	if first.headerHash != second.headerHash || a.Size > maxArchiveBytes || b.Size > maxArchiveBytes {
		return ErrLogAmbiguous
	}
	key := sha256.Sum256([]byte("archive-copy\x00" + first.path + "\x00" + second.path))
	k := hex.EncodeToString(key[:])
	if proof, ok := c.equivalences[k]; ok && proof.Plain.unchanged(a) && proof.Compressed.unchanged(b) {
		return nil
	}
	digest := func(path string, expected sourceStamp, decoded bool) ([32]byte, error) {
		var sum [32]byte
		f, err := os.Open(path)
		if err != nil {
			return sum, ErrHistoryChanged
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || stampFile(f, info) != expected {
			return sum, ErrHistoryChanged
		}
		var reader io.Reader = f
		limit := expected.Size
		if decoded {
			d, err := archiveDecoder(ctx, f, expected.Size)
			if err != nil {
				return sum, archiveError(ctx, err)
			}
			defer recycleDecoder(d)
			reader, limit = d, maxArchiveBytes
		}
		h := sha256.New()
		n, err := io.Copy(h, cancelReader{ctx, io.LimitReader(reader, limit+1)})
		if decoded && (err != nil || n > limit) {
			return sum, archiveError(ctx, ErrArchiveInvalid)
		}
		if err != nil {
			return sum, err
		}
		if !decoded && n != expected.Size {
			return sum, ErrHistoryChanged
		}
		h.Sum(sum[:0])
		return sum, nil
	}
	match := func(decoded bool) (bool, error) {
		left, err := digest(first.path, a, decoded)
		if err != nil {
			return false, err
		}
		right, err := digest(second.path, b, decoded)
		return left == right, err
	}
	var same bool
	var err error
	if a.Size == b.Size {
		same, err = match(false)
	}
	if err == nil && !same {
		same, err = match(true)
	}
	if err != nil {
		return err
	}
	currentA, ea := stampPath(first.path)
	currentB, eb := stampPath(second.path)
	if ea != nil || eb != nil || currentA != a || currentB != b {
		return ErrHistoryChanged
	}
	if !same {
		return ErrLogAmbiguous
	}
	c.rememberEquivalent(k, equivalence{a, b, -1})
	return nil
}

func (c *BoundaryCache) rememberEquivalent(key string, proof equivalence) {
	if c.equivalences == nil || len(c.equivalences) >= maxHistoryCandidates {
		c.equivalences = make(map[string]equivalence)
	}
	c.equivalences[key] = proof
	c.dirty = true
}

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
	c.rememberEquivalent(key, equivalence{a, b, end})
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

package rollout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
)

// Segments are ordered root to leaf. Only the last segment can grow (Bytes=-1).
// Fixed prefixes exclude reverted suffixes, including their usage and timings.
type Segment struct {
	Path                            string
	Bytes, StartOrdinal, EndOrdinal int64
	Header                          [32]byte
	LogicalID                       string
	Recovered                       bool
	Archive                         *archiveInfo `json:",omitempty"`
	AlternatePath                   string       `json:",omitempty"`
	Alternate                       *archiveInfo `json:",omitempty"`
}

type History struct {
	Head     string
	Segments []Segment
	sources  *BoundaryCache
}

// Ordinals identify retained records. Deletion may leave forward gaps, but a
// duplicate or reversal cannot establish the order of the selected history.
func advanceOrdinal(expected, observed int64) (int64, error) {
	if observed < 0 || observed < expected || observed == math.MaxInt64 {
		return expected, ErrHistoryOrdinal
	}
	return observed + 1, nil
}

func (h History) key() string {
	b, _ := json.Marshal(h)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func NewHistory(h History) *Reader {
	r := New(h.Head)
	if len(h.Segments) != 0 {
		h.Segments = append([]Segment(nil), h.Segments...)
		r.history = &h
		if h.sources == nil {
			r.history.sources = &BoundaryCache{}
		}
	}
	return r
}

func lineEndingAt(f io.ReaderAt, end int64) ([]byte, error) {
	if end <= 0 {
		return nil, ErrHistoryBoundary
	}
	for n := int64(4096); ; n *= 2 {
		n = min(n, end, int64(MaxRecordBytes+1))
		b := make([]byte, n)
		if _, err := f.ReadAt(b, end-n); err != nil || b[len(b)-1] != '\n' {
			return nil, ErrHistoryBoundary
		}
		start := bytes.LastIndexByte(b[:len(b)-1], '\n') + 1
		if start > 0 || n == end {
			if len(b)-start > MaxRecordBytes {
				return nil, ErrHistoryBoundary
			}
			return b[start:], nil
		}
		if n == int64(MaxRecordBytes+1) {
			return nil, ErrHistoryBoundary
		}
	}
}

func checkBoundary(ctx context.Context, f io.ReaderAt, size int64, s Segment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.StartOrdinal < 0 || s.Bytes < 0 || s.Bytes > size || s.EndOrdinal < s.StartOrdinal {
		return ErrHistoryBoundary
	}
	if s.Bytes == 0 {
		if s.EndOrdinal != s.StartOrdinal {
			return ErrHistoryBoundary
		}
		return nil
	}
	line, err := lineEndingAt(f, s.Bytes)
	if err != nil {
		return err
	}
	var record struct {
		Ordinal *int64 `json:"ordinal"`
	}
	if json.Unmarshal(line, &record) != nil || record.Ordinal == nil || *record.Ordinal < s.StartOrdinal || *record.Ordinal == math.MaxInt64 || *record.Ordinal+1 != s.EndOrdinal {
		return ErrHistoryBoundary
	}
	return ctx.Err()
}

func (r *Reader) RecoveredBoundaries() (n int) {
	if r.history != nil {
		for _, s := range r.history.Segments {
			if s.Recovered {
				n++
			}
		}
	}
	return n
}

type sourceFile interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
	Close() error
}

type historyFile struct {
	files           []*os.File
	starts, lengths []int64
	info            *historyInfo
	stamps          []sourceStamp
	readers         []io.ReaderAt
	archives        []*archiveFile
}

type historyInfo struct {
	os.FileInfo
	size  int64
	parts []os.FileInfo
	key   string
}

func (i *historyInfo) Size() int64 { return i.size }

func (r *Reader) openSource(ctx context.Context) (sourceFile, error) {
	if r.history == nil {
		if compressedPath(r.Path) {
			return openArchive(ctx, r.Path, r.archive)
		}
		return os.Open(r.Path)
	}
	h := &historyFile{info: &historyInfo{key: r.historyKey()}}
	fail := func(err error) (sourceFile, error) { h.Close(); return nil, err }
	for _, s := range r.history.Segments {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		f, err := os.Open(s.Path)
		if err != nil {
			return fail(ErrHistoryMissing)
		}
		h.files = append(h.files, f)
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return fail(ErrLogUnreadable)
		}
		stamp := stampFile(f, info)
		h.stamps = append(h.stamps, stamp)
		var data io.ReaderAt = f
		n := info.Size()
		if s.Archive != nil {
			a := s.Archive
			if !a.Stamp.unchanged(stamp) {
				checked, e := inspectArchive(ctx, f)
				if e != nil {
					return fail(e)
				}
				if checked.Size != a.Size || checked.Header != s.Header {
					return fail(ErrHistoryChanged)
				}
				a = &checked
			}
			z := &archiveFile{File: f, ctx: ctx, archive: *a}
			h.archives = append(h.archives, z)
			data = z
			n = a.Size
		}
		h.readers = append(h.readers, data)
		verified := len(r.proofs) > len(h.files)-1 && r.proofs[len(h.files)-1].Stamp.unchanged(stamp)
		if !verified {
			_, header, err := decodeHeader(ctx, io.NewSectionReader(data, 0, MaxRecordBytes+1))
			if err != nil {
				return fail(err)
			}
			if header != s.Header {
				return fail(ErrHistoryChanged)
			}
		}
		if s.Bytes >= 0 {
			if !verified {
				if err := checkBoundary(ctx, data, n, s); err != nil {
					return fail(err)
				}
			}
			n = s.Bytes
		}
		if z, ok := data.(*archiveFile); ok {
			z.release()
		}
		if n > math.MaxInt64-h.info.size {
			return fail(ErrHistoryBoundary)
		}
		h.starts = append(h.starts, h.info.size)
		h.lengths = append(h.lengths, n)
		h.info.size += n
		h.info.parts = append(h.info.parts, info)
		h.info.FileInfo = info
	}
	return h, nil
}

func (f *historyFile) Stat() (os.FileInfo, error) { return f.info, nil }
func (f *historyFile) Close() error {
	var err error
	for _, z := range f.archives {
		z.release()
	}
	f.archives = nil
	for _, file := range f.files {
		if e := file.Close(); e != nil {
			err = e
		}
	}
	f.files = nil
	return err
}

func (f *historyFile) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	for i, start := range f.starts {
		end := start + f.lengths[i]
		if offset >= end {
			continue
		}
		size := int(min(int64(len(p)-n), end-offset))
		got, err := f.readers[i].ReadAt(p[n:n+size], offset-start)
		n += got
		offset += int64(got)
		if offset == end {
			if z, ok := f.readers[i].(*archiveFile); ok {
				z.release()
			}
		}
		if err != nil {
			return n, err
		}
		if n == len(p) {
			return n, nil
		}
	}
	return n, io.EOF
}

func sameSource(a, b os.FileInfo) bool {
	x, ax := a.(*historyInfo)
	y, by := b.(*historyInfo)
	if ax != by {
		return false
	}
	if !ax {
		if d, ok := a.(*decodedInfo); ok {
			a = d.FileInfo
		}
		if d, ok := b.(*decodedInfo); ok {
			b = d.FileInfo
		}
		return os.SameFile(a, b)
	}
	if x.key != y.key || len(x.parts) != len(y.parts) {
		return false
	}
	for i := range x.parts {
		if !os.SameFile(x.parts[i], y.parts[i]) {
			return false
		}
	}
	return true
}

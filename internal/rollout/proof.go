package rollout

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"
	"time"
)

type prefixProof struct {
	Path   string
	Limit  int64
	Bytes  int64
	Digest [32]byte
	Stamp  sourceStamp
	hasher hash.Hash
}

func (p *prefixProof) sum() [32]byte {
	if p.hasher == nil {
		return p.Digest
	}
	var sum [32]byte
	p.hasher.Sum(sum[:0])
	return sum
}

type openedPart struct {
	file       *os.File
	path       string
	length     int64
	stamp      sourceStamp
	reader     io.ReaderAt
	compressed bool
}

func (r *Reader) sourceParts(source sourceFile, info os.FileInfo) []openedPart {
	if f, ok := source.(*historyFile); ok {
		parts := make([]openedPart, len(f.files))
		for i, file := range f.files {
			parts[i] = openedPart{file: file, path: r.history.Segments[i].Path, length: f.lengths[i], stamp: f.stamps[i], reader: f.readers[i], compressed: r.history.Segments[i].Archive != nil}
		}
		return parts
	}
	if f, ok := source.(*archiveFile); ok {
		a := f.archive
		r.archive = &a
		return []openedPart{{file: f.File, path: r.Path, length: a.Size, stamp: a.Stamp, reader: f, compressed: true}}
	}
	f := source.(*os.File)
	return []openedPart{{file: f, path: r.Path, length: info.Size(), stamp: stampFile(f, info), reader: f}}
}

func (r *Reader) startProofs(parts []openedPart) {
	r.proofs = make([]prefixProof, len(parts))
	for i, p := range parts {
		limit := int64(-1)
		if r.history != nil {
			limit = r.history.Segments[i].Bytes
		}
		r.proofs[i] = prefixProof{Path: p.path, Limit: limit, Stamp: p.stamp, hasher: sha256.New()}
	}
}

func (r *Reader) feedProofs(data []byte) {
	for i := range r.proofs {
		p := &r.proofs[i]
		if p.Limit >= 0 && p.Bytes == p.Limit {
			continue
		}
		n := len(data)
		if p.Limit >= 0 {
			n = min(n, int(p.Limit-p.Bytes))
		}
		if p.hasher == nil {
			p.hasher = sha256.New()
		} // New, empty segment only.
		p.hasher.Write(data[:n])
		p.Bytes += int64(n)
		data = data[n:]
		if len(data) == 0 {
			return
		}
	}
}

func currentPart(p openedPart) (sourceStamp, error) {
	info, err := p.file.Stat()
	if err != nil {
		return sourceStamp{}, err
	}
	current, err := os.Stat(p.path)
	if err != nil || !os.SameFile(info, current) {
		return sourceStamp{}, ErrHistoryChanged
	}
	return stampFile(p.file, info), nil
}

func (r *Reader) hashPart(ctx context.Context, p openedPart, n int64) (hash.Hash, error) {
	if z, ok := p.reader.(*archiveFile); ok {
		defer z.release()
	}
	if n < 0 {
		return nil, ErrHistoryChanged
	}
	h := sha256.New()
	if r.verifyBuffer == nil {
		r.verifyBuffer = make([]byte, 64<<10)
	}
	for offset := int64(0); offset < n; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count := int(min(int64(len(r.verifyBuffer)), n-offset))
		if _, err := p.reader.ReadAt(r.verifyBuffer[:count], offset); err != nil {
			return nil, err
		}
		h.Write(r.verifyBuffer[:count])
		offset += int64(count)
	}
	return h, ctx.Err()
}

// Each proof binds the bytes actually fed to aggregation. Unchanged reliable
// change stamps reuse the proof; changed/unsupported sources hash only their
// own consumed prefix. Publication and checkpoint restore use the same gate.
func (r *Reader) verifyProofs(ctx context.Context, parts []openedPart) error {
	started := time.Now()
	defer func() { r.VerifyTime += time.Since(started) }()
	if len(parts) != len(r.proofs) {
		return ErrHistoryChanged
	}
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := &r.proofs[i]
		if p.Path != part.path || p.Bytes < 0 || p.Bytes > part.length {
			return ErrHistoryChanged
		}
		if err := r.verifyPart(ctx, part, p, i); err != nil {
			return err
		}
	}
	// Hashing a later segment must not hide a rewrite of an earlier segment
	// whose proof was just checked. Recheck all cheap stamps before publishing.
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		stamp, err := currentPart(part)
		if err != nil {
			return err
		}
		if stamp != r.proofs[i].Stamp {
			if !part.compressed && stamp.Identity == r.proofs[i].Stamp.Identity && stamp.Size > r.proofs[i].Stamp.Size {
				return ErrSourceBusy
			}
			return ErrHistoryChanged
		}
		if r.history != nil && r.proofs[i].Bytes > 0 {
			s := r.history.Segments[i]
			if s.Alternate != nil {
				stamp, e := stampPath(s.AlternatePath)
				if e != nil || stamp != s.Alternate.Stamp {
					return ErrHistoryChanged
				}
				if !stamp.Trusted {
					if e := r.history.sources.equivalent(ctx, logNode{path: s.Path}, logNode{path: s.AlternatePath, archive: s.Alternate}, s); e != nil {
						return e
					}
				}
			}
		}
	}
	return ctx.Err()
}

func (r *Reader) verifyPart(ctx context.Context, part openedPart, p *prefixProof, index int) error {
	before, err := currentPart(part)
	if err != nil {
		return err
	}
	if !part.compressed && before.Size < p.Bytes {
		return ErrHistoryChanged
	}
	if p.Stamp.unchanged(before) {
		return nil
	}
	h, err := r.hashPart(ctx, part, p.Bytes)
	if err != nil {
		return err
	}
	after, err := currentPart(part)
	if err != nil {
		return err
	}
	var sum [32]byte
	h.Sum(sum[:0])
	if sum != p.sum() {
		return ErrHistoryChanged
	}
	if before != after {
		if !part.compressed && before.Identity == after.Identity && after.Size > before.Size {
			return ErrSourceBusy
		}
		return ErrHistoryChanged
	}
	if p.Bytes == 0 && r.history != nil {
		_, header, err := decodeHeader(ctx, io.NewSectionReader(part.reader, 0, MaxRecordBytes+1))
		if err != nil || header != r.history.Segments[index].Header {
			return ErrHistoryChanged
		}
	}
	p.Stamp = after
	if p.hasher == nil && (p.Limit < 0 || p.Bytes < p.Limit) {
		p.hasher = h
	}
	return nil
}

// Hash state is never serialized: it can contain up to 63 source bytes. After
// restore, reconstruct only the growing segment, and only when it gets new data.
func (r *Reader) prepareAppend(ctx context.Context, parts []openedPart) error {
	for i := range r.proofs {
		p := &r.proofs[i]
		if p.hasher != nil || p.Limit >= 0 && p.Bytes == p.Limit {
			continue
		}
		h, err := r.hashPart(ctx, parts[i], p.Bytes)
		if err != nil {
			return err
		}
		var sum [32]byte
		h.Sum(sum[:0])
		if sum != p.Digest {
			return ErrHistoryChanged
		}
		p.hasher = h
	}
	return nil
}

func (r *Reader) sourceFailure(err error) error {
	if !errors.Is(err, ErrSourceBusy) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.failure = ErrHistoryChanged
		if errors.Is(err, ErrArchiveInvalid) {
			r.failure = ErrArchiveInvalid
		}
		r.State.MarkIncomplete()
	}
	return err
}

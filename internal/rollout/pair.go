package rollout

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"strings"
	"time"

	"codex-usage-monitor/internal/metrics"
)

type pairChunk struct {
	digest     hash.Hash
	start, end int64
	skip       bool
}
type pairCursor struct {
	file    *archiveFile
	reader  *bufio.Reader
	next    int64
	scratch []byte
}

func semanticRecord(line []byte) (metrics.Record, error) {
	r, err := metrics.DecodeRecord(line)
	if err != nil {
		var typed *json.UnmarshalTypeError
		if r.Type == "" || r.Type == "event_msg" || r.Type == "token_usage_record" || r.Type == "session_meta" || !errors.As(err, &typed) || !strings.HasPrefix(typed.Field, "payload") {
			return r, ErrLogAmbiguous
		}
	}
	return r, nil
}

func writeSemantic(h hash.Hash, r metrics.Record) error {
	sum := r.SemanticHash()
	h.Write(sum[:])
	return nil
}

func equivalenceKey(s Segment) string {
	b, _ := json.Marshal([]any{s.Path, s.AlternatePath, s.StartOrdinal, s.EndOrdinal, s.Bytes})
	key := sha256.Sum256(b)
	return hex.EncodeToString(key[:])
}

func (r *Reader) observePair(record metrics.Record, expected int64) error {
	if r.history == nil {
		return nil
	}
	var start int64
	for i, s := range r.history.Segments {
		if s.Bytes == 0 {
			continue
		}
		if s.Bytes >= 0 && r.lookupEnd >= start+s.Bytes {
			start += s.Bytes
			continue
		}
		if s.Alternate == nil {
			return nil
		}
		if record.Ordinal == nil {
			return ErrLogAmbiguous
		}
		if r.pairs == nil {
			r.pairs = make(map[int]*pairChunk)
		}
		p := r.pairs[i]
		if p == nil {
			// Include the gap before this chunk so an extra record in the
			// alternate cannot disappear between two verified chunks.
			p = &pairChunk{start: expected, digest: sha256.New()}
			r.pairs[i] = p
			if proof, ok := r.history.sources.equivalences[equivalenceKey(s)]; ok && proof.End == s.EndOrdinal && proof.Plain.unchanged(r.proofs[i].Stamp) && proof.Compressed.unchanged(s.Alternate.Stamp) {
				p.skip = true
			}
		}
		p.end = *record.Ordinal + 1
		if p.skip {
			return nil
		}
		return writeSemantic(p.digest, record)
	}
	return nil
}

func (p *pairCursor) rangeDigest(ctx context.Context, start, end int64) ([32]byte, error) {
	if p.next > start {
		p.file.release()
		p.reader.Reset(io.NewSectionReader(p.file, 0, p.file.archive.Size))
		p.next = *p.file.archive.Meta.Ordinal
	}
	h := sha256.New()
	for p.next < end {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		line, err := p.reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			p.scratch = appendRecord(p.scratch[:0], line)
			for err == bufio.ErrBufferFull {
				if err := ctx.Err(); err != nil {
					return [32]byte{}, err
				}
				line, err = p.reader.ReadSlice('\n')
				if len(p.scratch)+len(line) > MaxRecordBytes {
					return [32]byte{}, ErrLogAmbiguous
				}
				p.scratch = appendRecord(p.scratch, line)
			}
			line = p.scratch
		}
		if err != nil {
			return [32]byte{}, ErrLogAmbiguous
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		r, err := semanticRecord(line)
		if err != nil || r.Ordinal == nil || *r.Ordinal >= end {
			return [32]byte{}, ErrLogAmbiguous
		}
		next, err := advanceOrdinal(p.next, *r.Ordinal)
		if err != nil {
			return [32]byte{}, ErrLogAmbiguous
		}
		if *r.Ordinal >= start {
			if err := writeSemantic(h, r); err != nil {
				return [32]byte{}, err
			}
		}
		p.next = next
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum, nil
}

// Both representations are read once in ordinal order. The primary stream
// feeds aggregation and its digest together; no second JSON replay is needed.
func (r *Reader) verifyPairs(ctx context.Context) error {
	if len(r.pairs) == 0 {
		return nil
	}
	started := time.Now()
	defer func() { r.VerifyTime += time.Since(started) }()
	if r.pairReaders == nil {
		r.pairReaders = make(map[int]*pairCursor)
	}
	for i, s := range r.history.Segments {
		p := r.pairs[i]
		if p == nil {
			continue
		}
		if p.skip {
			proof := r.history.sources.equivalences[equivalenceKey(s)]
			a, ea := stampPath(s.Path)
			b, eb := stampPath(s.AlternatePath)
			if ea != nil || eb != nil || !proof.Plain.unchanged(a) || !proof.Compressed.unchanged(b) {
				return ErrHistoryChanged
			}
			continue
		}
		cursor := r.pairReaders[i]
		if cursor == nil {
			known := s.Alternate
			if a, ok := r.history.sources.archives[s.AlternatePath]; ok && a.Size > 0 && a.Stamp == known.Stamp {
				known = &a
			}
			z, err := openArchive(ctx, s.AlternatePath, known)
			if err != nil {
				return err
			}
			if z.archive.Meta.Ordinal == nil {
				z.Close()
				return ErrLogAmbiguous
			}
			if r.history.sources.archives == nil {
				r.history.sources.archives = make(map[string]archiveInfo)
			}
			r.history.sources.archives[s.AlternatePath] = z.archive
			r.history.sources.dirty = true
			cursor = &pairCursor{file: z, reader: bufio.NewReaderSize(io.NewSectionReader(z, 0, z.archive.Size), 64<<10), next: *z.archive.Meta.Ordinal}
			r.pairReaders[i] = cursor
		}
		sum, err := cursor.rangeDigest(ctx, p.start, p.end)
		if err != nil {
			return err
		}
		if !bytes.Equal(sum[:], p.digest.Sum(nil)) {
			return ErrLogAmbiguous
		}
		complete := s.EndOrdinal >= 0 && p.end == s.EndOrdinal || s.EndOrdinal < 0 && r.info != nil && r.Offset == r.info.Size()
		if complete {
			if s.EndOrdinal < 0 {
				for {
					if e := ctx.Err(); e != nil {
						return e
					}
					part, e := cursor.reader.ReadSlice('\n')
					if len(bytes.TrimSpace(part)) > 0 {
						return ErrLogAmbiguous
					}
					if e == io.EOF {
						break
					}
					if e != nil && e != bufio.ErrBufferFull {
						return ErrLogAmbiguous
					}
				}
			}
			if r.history.sources.equivalences == nil || len(r.history.sources.equivalences) >= maxHistoryCandidates {
				r.history.sources.equivalences = make(map[string]equivalence)
			}
			r.history.sources.equivalences[equivalenceKey(s)] = equivalence{r.proofs[i].Stamp, s.Alternate.Stamp, s.EndOrdinal}
			r.history.sources.dirty = true
			cursor.file.Close()
			delete(r.pairReaders, i)
		}
	}
	return nil
}

func (r *Reader) closePairs() {
	for _, p := range r.pairReaders {
		p.file.Close()
	}
	r.pairReaders = nil
	r.pairs = nil
}

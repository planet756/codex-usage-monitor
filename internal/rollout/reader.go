// Package rollout implements bounded, cancellation-aware JSONL replay and
// incremental updates. Codex files are opened read-only and never modified.
package rollout

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"time"

	"codex-usage-monitor/internal/metrics"
)

const MaxRecordBytes = 8 << 20

type Reader struct {
	Path          string
	State         *metrics.State
	Offset        int64
	info          os.FileInfo
	pending       []byte
	dropping      bool
	buffer        *bufio.Reader
	proofs        []prefixProof
	archive       *archiveInfo
	chunkSource   sourceFile
	chunkContext  context.Context
	VerifyTime    time.Duration
	rebuilds      uint64
	pairs         map[int]*pairChunk
	pairReaders   map[int]*pairCursor
	verifyBuffer  []byte
	lookupFile    io.ReaderAt
	lookupContext context.Context
	lookupEnd     int64
	closed        bool
	history       *History
	sourceID      string
	ordinalKnown  bool
	nextOrdinal   int64
	failure       error
	seen          []uint64
	seenIndex     *turnIndex
}

func New(path string) *Reader {
	r := &Reader{Path: path, State: metrics.New()}
	return r
}

func (r *Reader) Update(ctx context.Context) (bool, error) {
	return r.update(ctx, 0)
}

// UpdateChunk yields only at full JSONL records or explicit segment cuts.
// Every yielded snapshot passes the same source verification as a full replay.
func (r *Reader) UpdateChunk(ctx context.Context) (bool, error) { return r.update(ctx, 64<<20) }

func (r *Reader) update(ctx context.Context, budget int64) (updated bool, resultErr error) {
	defer func() {
		if resultErr != nil || !r.Loading() {
			clearDecoders()
		}
	}()
	started := time.Now()
	initialOffset := r.Offset
	if r.closed {
		return false, os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		r.closeChunk()
		r.closePairs()
		return false, err
	}
	if r.chunkSource != nil && (budget == 0 || reflect.TypeOf(ctx) != reflect.TypeOf(r.chunkContext) || !reflect.TypeOf(ctx).Comparable() || ctx != r.chunkContext) {
		r.closeChunk()
		r.closePairs()
	}
	f := r.chunkSource
	continued := f != nil
	r.chunkSource = nil
	r.chunkContext = nil
	var err error
	if f == nil {
		f, err = r.openSource(ctx)
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if budget > 0 && resultErr == nil && ctx.Err() == nil && r.info != nil && r.Offset < r.info.Size() {
			r.chunkSource, r.chunkContext = f, ctx
		} else {
			f.Close()
			r.closePairs()
		}
	}()
	r.pairs = nil
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	changed := false
	if r.info != nil && !continued {
		matches, err := r.matchesContext(ctx, f, info)
		if err != nil {
			return false, err
		}
		if !matches || r.failure != nil && !permanentAccountingFailure(r.failure) {
			r.rebuilds++
			r.State = metrics.New()
			r.Offset = 0
			r.pending = nil
			r.dropping = false
			r.proofs = nil
			r.closeSeenIndex()
			r.seen = nil
			r.ordinalKnown, r.nextOrdinal, r.failure = false, 0, nil
			changed = true
		}
	}
	r.info = info
	if r.failure != nil {
		return false, r.failure
	}
	if info.Size() == r.Offset {
		return changed, nil
	}
	parts := r.sourceParts(f, info)
	if len(r.proofs) == 0 {
		r.startProofs(parts)
	}
	if err := r.prepareAppend(ctx, parts); err != nil {
		return changed, r.sourceFailure(err)
	}
	section := io.NewSectionReader(f, r.Offset, info.Size()-r.Offset)
	if r.buffer == nil {
		r.buffer = bufio.NewReaderSize(section, 64<<10)
	} else {
		r.buffer.Reset(section)
	}
	br := r.buffer
	defer r.buffer.Reset(nil)
	defer func() {
		if len(r.pending) == 0 && cap(r.pending) > 256<<10 && (resultErr != nil || !r.Loading()) {
			r.pending = nil
		}
	}()
	r.lookupFile, r.lookupContext = f, ctx
	defer func() { r.lookupFile, r.lookupContext = nil, nil }()
	for {
		if err = ctx.Err(); err != nil {
			return changed, err
		}
		part, e := br.ReadSlice('\n')
		r.Offset += int64(len(part))
		if !r.dropping && len(r.pending) == 0 && e == nil {
			r.feedProofs(part)
			if len(bytes.TrimSpace(part)) > 0 {
				r.lookupEnd = r.Offset - int64(len(part))
				if err := r.observe(part); err != nil {
					return changed, err
				}
				changed = true
			}
			if budget > 0 && (r.Offset-initialOffset >= budget || time.Since(started) >= 250*time.Millisecond || r.Boundary() > 0) {
				return r.finishUpdate(ctx, parts, changed)
			}
			continue
		}
		if r.dropping {
			r.feedProofs(part)
		} else {
			if len(r.pending)+len(part) > MaxRecordBytes {
				r.feedProofs(r.pending)
				r.feedProofs(part)
				r.pending = nil
				r.dropping = true
				r.State.Oversized++
				r.State.MarkIncomplete()
				changed = true
			} else {
				r.pending = appendRecord(r.pending, part)
			}
		}
		if len(part) > 0 && part[len(part)-1] == '\n' {
			if r.dropping && r.ordinalKnown {
				// The record remains explicitly incomplete. Counting its one JSONL
				// slot lets later verified records provide useful lower bounds.
				if r.nextOrdinal == math.MaxInt64 {
					r.failure = ErrHistoryOrdinal
					return changed, r.failure
				}
				r.nextOrdinal++
			}
			if !r.dropping {
				r.feedProofs(r.pending)
				if len(bytes.TrimSpace(r.pending)) > 0 {
					r.lookupEnd = r.Offset - int64(len(r.pending))
					if err := r.observe(r.pending); err != nil {
						return changed, err
					}
					changed = true
				}
			}
			r.pending = r.pending[:0]
			r.dropping = false
			// Reuse large scratch within this read, release it on return.
			if budget > 0 && (r.Offset-initialOffset >= budget || time.Since(started) >= 250*time.Millisecond || r.Boundary() > 0) {
				return r.finishUpdate(ctx, parts, changed)
			}
		}
		if errors.Is(e, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(e, io.EOF) {
			if r.Offset < info.Size() {
				return changed, io.ErrUnexpectedEOF
			}
			if len(r.pending) > 0 && len(parts) > 0 && parts[len(parts)-1].compressed {
				return false, r.sourceFailure(ErrArchiveInvalid)
			}
			return r.finishUpdate(ctx, parts, changed)
		}
		if e != nil {
			return changed, e
		}
	}
}

func (r *Reader) finishUpdate(ctx context.Context, parts []openedPart, changed bool) (bool, error) {
	if err := r.verifyPairs(ctx); err != nil {
		r.State.MarkIncomplete()
		r.failure = err
		return false, err
	}
	if err := r.verifyProofs(ctx, parts); err != nil {
		return false, r.sourceFailure(err)
	}
	return changed, nil
}

func permanentAccountingFailure(err error) bool {
	return errors.Is(err, ErrArchiveInvalid) || errors.Is(err, ErrLogAmbiguous) || errors.Is(err, ErrHistoryOrdinal) || errors.Is(err, ErrAccountingConflict) || errors.Is(err, ErrLogIdentityUnverified)
}

// Close releases the optional exact historical index. Source logs stay read-only.
func (r *Reader) Close() {
	defer clearDecoders()
	r.closeChunk()
	r.closePairs()
	r.closeSeenIndex()
	r.closed = true
	r.pending, r.verifyBuffer = nil, nil
	r.archive = nil
	r.buffer, r.proofs = nil, nil
	r.lookupFile, r.lookupContext = nil, nil
	r.seen, r.history = nil, nil
	r.info, r.failure = nil, nil
	r.sourceID = ""
}

func (r *Reader) closeChunk() {
	if r.chunkSource != nil {
		r.chunkSource.Close()
	}
	r.chunkSource = nil
	r.chunkContext = nil
}

func (r *Reader) observe(line []byte) error {
	beforeOrdinal, beforeMalformed := r.nextOrdinal, r.State.Malformed
	err := r.State.ObserveWith(line, func(record *metrics.Record) (bool, error) {
		if r.history != nil {
			var start int64
			for _, seg := range r.history.Segments {
				if seg.Bytes == 0 {
					continue
				}
				if seg.Bytes < 0 || r.lookupEnd < start+seg.Bytes {
					atHeader := r.lookupEnd == start
					if atHeader && (record.Type != "session_meta" || !strings.EqualFold(record.Payload.ID, seg.LogicalID) || sha256.Sum256(line) != seg.Header) || !atHeader && record.Type == "session_meta" {
						return false, ErrLogIdentityUnverified
					}
					break
				}
				start += seg.Bytes
			}
		} else if record.Type == "session_meta" && r.State.ThreadID != "" {
			return false, ErrLogIdentityUnverified
		}
		expected := r.nextOrdinal
		if record.Ordinal != nil {
			next, err := advanceOrdinal(expected, *record.Ordinal)
			if err != nil {
				return false, err
			}
			if missing := *record.Ordinal - expected; missing > 0 {
				r.State.MarkHistoryGap(missing)
			}
			r.ordinalKnown, r.nextOrdinal = true, next
		} else if r.ordinalKnown {
			return false, ErrHistoryOrdinal
		}
		if err := r.observePair(*record, expected); err != nil {
			return false, err
		}
		if r.history != nil && record.Type == "session_meta" && record.Ordinal == nil {
			r.State.MarkIncomplete() // A header without sequence evidence cannot certify a full history.
		}
		if r.history == nil {
			if record.Type == "session_meta" && len(record.Payload.HistoryBase) > 0 && string(record.Payload.HistoryBase) != "null" {
				r.State.MarkIncomplete()
			}
		}
		indexBefore := r.seenIndex
		accepted, err := r.State.Admit(record, r.seenBefore)
		// A lazily rebuilt index covers the preceding prefix; include this record's
		// earlier keys when a later lookup created that index.
		if r.seenIndex != nil && r.seenIndex != indexBefore {
			for _, key := range record.AccountingKeys(r.State.ThreadID) {
				if key != "" {
					if e := r.seenIndex.add(key); e != nil {
						r.closeSeenIndex()
						break
					}
				}
			}
		}
		return accepted, err
	})
	if err != nil {
		r.failure = err
	} else if r.ordinalKnown && r.State.Malformed > beforeMalformed && r.nextOrdinal == beforeOrdinal {
		if r.nextOrdinal == math.MaxInt64 {
			r.failure = ErrHistoryOrdinal
			return r.failure
		}
		r.nextOrdinal++ // Malformed records already made HistoryComplete false.
	}
	return err
}

package rollout

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"codex-usage-monitor/internal/metrics"
)

const seenWords = 4096

func seenBits(key string) [4]uint32 {
	h := sha256.Sum256([]byte(key))
	var bits [4]uint32
	for i := range bits {
		bits[i] = binary.LittleEndian.Uint32(h[i*4:]) % (seenWords * 64)
	}
	return bits
}

func (r *Reader) closeSeenIndex() {
	if r.seenIndex != nil {
		r.seenIndex.close()
		r.seenIndex = nil
	}
}

func (r *Reader) seenBefore(key string) (bool, error) {
	if r.seen == nil {
		r.seen = make([]uint64, seenWords)
	}
	bits := seenBits(key)
	maybe := true
	for _, bit := range bits {
		maybe = maybe && r.seen[bit/64]&(uint64(1)<<(bit%64)) != 0
	}
	if maybe {
		if r.seenIndex != nil {
			found, err := r.seenIndex.contains(key)
			if err != nil {
				r.closeSeenIndex()
			} else if found {
				return true, nil
			}
		}
		if r.seenIndex == nil {
			found, err := r.rebuildSeen(key)
			if err != nil || found {
				return found, err
			}
		}
	}
	for _, bit := range bits {
		r.seen[bit/64] |= uint64(1) << (bit % 64)
	}
	if r.seenIndex != nil {
		if err := r.seenIndex.add(key); err != nil {
			r.closeSeenIndex()
		}
	}
	return false, nil
}

func (r *Reader) rebuildSeen(wanted string) (bool, error) {
	if r.lookupFile == nil || r.lookupContext == nil {
		return false, errors.New("history witness unavailable")
	}
	index, _ := newTurnIndex()
	defer func() {
		if index != nil {
			index.close()
		}
	}()
	thread := ""
	found := false
	err := scanPrefix(r.lookupContext, r.lookupFile, r.lookupEnd, func(line []byte) error {
		record, err := metrics.DecodeRecord(line)
		if err != nil || !record.ValidCarrier() {
			return nil
		}
		if record.Type == "session_meta" {
			thread = record.Payload.ID
		}
		keys := record.AccountingKeys(thread)
		for _, key := range keys {
			if key == "" {
				continue
			}
			found = found || key == wanted
			if index != nil {
				if err := index.add(key); err != nil {
					index.close()
					index = nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	r.seenIndex, index = index, nil
	return found, nil
}

// scanPrefix owns bounded scratch space and never retains message contents.
func scanPrefix(ctx context.Context, source io.ReaderAt, end int64, visit func([]byte) error) error {
	br := bufio.NewReaderSize(io.NewSectionReader(source, 0, end), 64<<10)
	var pending []byte
	dropping := false
	var scanned int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		part, err := br.ReadSlice('\n')
		scanned += int64(len(part))
		var line []byte
		if !dropping && len(pending) == 0 && err == nil {
			line = part
		} else if !dropping {
			if len(pending)+len(part) > MaxRecordBytes {
				pending = nil
				dropping = true
			} else {
				pending = appendRecord(pending, part)
				if err == nil {
					line = pending
				}
			}
		}
		if line != nil {
			if err := visit(line); err != nil {
				return err
			}
		}
		if err == nil {
			pending, dropping = pending[:0], false
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			if scanned != end {
				return io.ErrUnexpectedEOF
			}
			return ctx.Err()
		}
		if err != nil {
			return err
		}
	}
}

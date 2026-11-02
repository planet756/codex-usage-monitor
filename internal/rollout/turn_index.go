package rollout

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
)

const indexBuckets = 8192
const indexRecord = sha256.Size + 8

// A private hash-chain working index uses fixed RAM and batched disk writes.
// SHA-256 witnesses replace probabilistic filter decisions. A checksummed
// sidecar accelerates resume; missing sidecars rebuild from the verified prefix.
type turnIndex struct {
	file    *os.File
	heads   [indexBuckets]int64
	pending []byte
	flushed int64
}

func newTurnIndex() (*turnIndex, error) {
	f, err := temporaryIndex()
	if err != nil {
		return nil, err
	}
	return &turnIndex{file: f, pending: make([]byte, 0, indexRecord*256)}, nil
}

func (x *turnIndex) close() {
	x.file.Close()
}

func (x *turnIndex) add(id string) error {
	if len(x.pending)+indexRecord > cap(x.pending) {
		if _, err := x.file.WriteAt(x.pending, x.flushed); err != nil {
			return err
		}
		x.flushed += int64(len(x.pending))
		x.pending = x.pending[:0]
	}
	sum := sha256.Sum256([]byte(id))
	bucket := binary.LittleEndian.Uint16(sum[:2]) % indexBuckets
	var record [indexRecord]byte
	copy(record[:], sum[:])
	binary.LittleEndian.PutUint64(record[sha256.Size:], uint64(x.heads[bucket]))
	x.heads[bucket] = x.flushed + int64(len(x.pending)) + 1
	x.pending = append(x.pending, record[:]...)
	return nil
}

func (x *turnIndex) contains(id string) (bool, error) {
	sum := sha256.Sum256([]byte(id))
	bucket := binary.LittleEndian.Uint16(sum[:2]) % indexBuckets
	var record [indexRecord]byte
	for pos := x.heads[bucket]; pos != 0; {
		at := pos - 1
		if at < 0 || at%indexRecord != 0 || at+indexRecord > x.flushed+int64(len(x.pending)) {
			return false, errors.New("invalid temporary turn index")
		}
		if at >= x.flushed {
			copy(record[:], x.pending[at-x.flushed:at-x.flushed+indexRecord])
		} else if _, err := x.file.ReadAt(record[:], at); err != nil {
			return false, err
		}
		if bytes.Equal(record[:sha256.Size], sum[:]) {
			return true, nil
		}
		next := int64(binary.LittleEndian.Uint64(record[sha256.Size:]))
		if next >= pos {
			return false, errors.New("invalid temporary turn index chain")
		}
		pos = next
	}
	return false, nil
}

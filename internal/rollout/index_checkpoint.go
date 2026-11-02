package rollout

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codex-usage-monitor/internal/storage"
)

const MaxIndexCheckpointBytes = 16 << 20

type indexCheckpoint struct {
	Bytes  int64
	Digest [32]byte
}

func indexCachePath(path, kind string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + "." + kind
}

// Only SHA-256 membership keys and chain offsets are cached. The working copy
// remains a private delete-on-close file; there is no persisted chat content.
func saveIndex(ctx context.Context, path string, x *turnIndex) (*indexCheckpoint, error) {
	if x == nil {
		return nil, nil
	}
	size := x.flushed + int64(len(x.pending))
	if size+indexBuckets*8 > MaxIndexCheckpointBytes {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cum-index-*.tmp")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	h := sha256.New()
	writer := io.MultiWriter(f, h)
	var heads [indexBuckets * 8]byte
	for i, v := range x.heads {
		binary.LittleEndian.PutUint64(heads[i*8:], uint64(v))
	}
	_, err = writer.Write(heads[:])
	if err == nil {
		err = copyIndex(ctx, writer, io.NewSectionReader(x.file, 0, x.flushed), x.flushed)
	}
	if err == nil {
		_, err = writer.Write(x.pending)
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := storage.Replace(name, path); err != nil {
		return nil, err
	}
	proof := &indexCheckpoint{Bytes: size}
	h.Sum(proof.Digest[:0])
	return proof, nil
}

func restoreIndex(ctx context.Context, path string, p *indexCheckpoint) *turnIndex {
	if p == nil || p.Bytes < 0 || p.Bytes%indexRecord != 0 || p.Bytes+indexBuckets*8 > MaxIndexCheckpointBytes {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != p.Bytes+indexBuckets*8 {
		return nil
	}
	x, err := newTurnIndex()
	if err != nil {
		return nil
	}
	valid := false
	defer func() {
		if !valid {
			x.close()
		}
	}()
	h := sha256.New()
	reader := io.TeeReader(f, h)
	var heads [indexBuckets * 8]byte
	if _, err := io.ReadFull(reader, heads[:]); err != nil {
		return nil
	}
	for i := range x.heads {
		v := int64(binary.LittleEndian.Uint64(heads[i*8:]))
		if v < 0 || v > p.Bytes || v != 0 && (v-1)%indexRecord != 0 {
			return nil
		}
		x.heads[i] = v
	}
	if copyIndex(ctx, x.file, reader, p.Bytes) != nil {
		return nil
	}
	var digest [32]byte
	h.Sum(digest[:0])
	if digest != p.Digest {
		return nil
	}
	x.flushed = p.Bytes
	valid = true
	return x
}

func copyIndex(ctx context.Context, w io.Writer, r io.Reader, n int64) error {
	buf := make([]byte, 64<<10)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := int(min(n, int64(len(buf))))
		if _, err := io.ReadFull(r, buf[:size]); err != nil {
			return err
		}
		written, err := w.Write(buf[:size])
		if err != nil {
			return err
		}
		if written != size {
			return errors.New("short index write")
		}
		n -= int64(size)
	}
	return ctx.Err()
}

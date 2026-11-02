package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestOversizedFrameIsClassifiedAndRawBuffersReleaseOnError(t *testing.T) {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], 1<<20)
	var r FrameReader
	if _, err := r.Read(bytes.NewReader(append(header[:], 'x'))); err == nil || cap(r.scratch) != 0 {
		t.Fatal("failed payload retained its large raw buffer")
	}
	binary.LittleEndian.PutUint32(header[:], MaxFrame+1)
	wire := append(header[:], bytes.Repeat([]byte{'x'}, MaxFrame+1)...)
	if _, err := r.Read(bytes.NewReader(wire)); !errors.Is(err, ErrFrameOversized) || !errors.Is(err, ErrFrameSkipped) {
		t.Fatalf("large frame lost its recovery classification: %v", err)
	}
}

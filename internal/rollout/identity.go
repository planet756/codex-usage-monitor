package rollout

import (
	"bytes"
	"context"
	"errors"
	"os"
)

func (r *Reader) matchesContext(ctx context.Context, f sourceFile, info os.FileInfo) (bool, error) {
	if r.info == nil || !sameSource(r.info, info) || info.Size() < r.Offset || info.Size() < r.info.Size() {
		return false, nil
	}
	err := r.verifyProofs(ctx, r.sourceParts(f, info))
	if errors.Is(err, ErrHistoryChanged) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	offset := r.Offset - int64(len(r.pending))
	if len(r.pending) > 0 && r.verifyBuffer == nil {
		r.verifyBuffer = make([]byte, 64<<10)
	}
	for at := 0; at < len(r.pending); {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n := min(len(r.verifyBuffer), len(r.pending)-at)
		if _, err := f.ReadAt(r.verifyBuffer[:n], offset+int64(at)); err != nil {
			return false, err
		}
		if !bytes.Equal(r.verifyBuffer[:n], r.pending[at:at+n]) {
			return false, nil
		}
		at += n
	}
	return true, nil
}

// Append only within the record budget; Go's normal geometric growth can keep
// almost 10 MiB allocated for an 8 MiB pending record.
func appendRecord(dst, part []byte) []byte {
	need := len(dst) + len(part)
	if need > cap(dst) {
		next := make([]byte, len(dst), min(MaxRecordBytes, max(need, cap(dst)*2)))
		copy(next, dst)
		dst = next
	}
	return append(dst, part...)
}

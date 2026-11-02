package rollout

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
)

func checkpointPath(dir, path string) string {
	return filepath.Join(dir, "cache", fmt.Sprintf("%x.json", sha256.Sum256([]byte(path))))
}

func prefixPath(dir string, h History, n int) string {
	p := History{Head: h.Segments[n-1].Path, Segments: h.Segments[:n]}
	return filepath.Join(dir, "cache", fmt.Sprintf("%x.json", sha256.Sum256([]byte("prefix\x00"+p.key()))))
}

func (r *Reader) SaveBoundary(ctx context.Context, dir string) error {
	n := r.Boundary()
	if n == 0 {
		return nil
	}
	return r.saveCheckpoint(ctx, prefixPath(dir, *r.history, n), n)
}

// Boundary reports a completed inherited segment cut, never an approximate
// byte position or a partly parsed record. Prefix state carries open turns and
// exact membership so cross-segment responses and timing are not summed twice.
func (r *Reader) Boundary() int {
	if r.history == nil || len(r.pending) > 0 {
		return 0
	}
	var offset int64
	for i, s := range r.history.Segments {
		if s.Bytes < 0 {
			return 0
		}
		offset += s.Bytes
		if offset == r.Offset && i < len(r.history.Segments)-1 {
			return i + 1
		}
	}
	return 0
}

func (r *Reader) Loading() bool {
	if r.info == nil {
		return false
	}
	if r.Offset < r.info.Size() {
		return true
	}
	if len(r.proofs) == 0 {
		return false
	}
	i := len(r.proofs) - 1
	if r.proofs[i].Limit >= 0 || compressedPath(r.proofs[i].Path) {
		return false
	}
	size := r.info.Size()
	if h, ok := r.info.(*historyInfo); ok {
		size = h.parts[i].Size()
	}
	return r.proofs[i].Stamp.Size > size
}

func (r *Reader) acceptsPrefix(c checkpoint) bool {
	if r.history == nil || c.Plan == nil || len(c.Plan.Segments) == 0 || len(c.Plan.Segments) != len(c.Parts) || len(c.Parts) > len(r.history.Segments) {
		return false
	}
	for i, old := range c.Plan.Segments {
		now := r.history.Segments[i]
		if !sameRepresentationPath(old.Path, now.Path) || c.Parts[i].Path != old.Path || old.Header != now.Header || old.LogicalID != now.LogicalID || old.StartOrdinal != now.StartOrdinal || c.Parts[i].Limit != old.Bytes || old.AlternatePath != now.AlternatePath || !reflect.DeepEqual(old.Alternate, now.Alternate) {
			return false
		}
		if i < len(c.Parts)-1 && (old.Bytes != now.Bytes || old.EndOrdinal != now.EndOrdinal) {
			return false
		}
		if now.Bytes >= 0 && c.Parts[i].Bytes > now.Bytes {
			return false
		}
	}
	return c.Offset > 0
}

func sameRepresentationPath(a, b string) bool {
	return plainArchivePath(a) == plainArchivePath(b)
}

func (r *Reader) RestoreBest(ctx context.Context, dir string) (string, bool) {
	if r.RestoreCheckpointContext(ctx, checkpointPath(dir, r.Path)) {
		return "checkpoint", true
	}
	if ctx.Err() != nil || r.history == nil {
		return "rebuild", false
	}
	h := *r.history
	sibling := r.Path + ".zst"
	if compressedPath(r.Path) {
		sibling = plainArchivePath(r.Path)
	}
	if r.restoreCheckpoint(ctx, checkpointPath(dir, sibling), true) {
		return "prefix", true
	}
	for n := len(h.Segments) - 1; n > 0; n-- {
		for _, path := range []string{prefixPath(dir, h, n), checkpointPath(dir, h.Segments[n-1].Path)} {
			if r.restoreCheckpoint(ctx, path, true) {
				return "prefix", true
			}
			if ctx.Err() != nil {
				return "rebuild", false
			}
		}
	}
	return "rebuild", false
}

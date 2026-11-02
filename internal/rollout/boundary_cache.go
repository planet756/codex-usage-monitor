package rollout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"codex-usage-monitor/internal/storage"
)

type boundaryRecord struct {
	Path                           string
	Header                         [32]byte
	Start, End, Declared, Resolved int64
	Stamp                          sourceStamp
}

type boundaryStore struct {
	Format       string
	Entries      []boundaryRecord
	Archives     map[string]archiveInfo
	Equivalences map[string]equivalence
	Integrity    string
}

func (b boundaryStore) digest() string {
	b.Integrity = ""
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func BoundaryCachePath(dir, id string) string {
	sum := sha256.Sum256([]byte("boundary-mapping\x00" + id))
	return filepath.Join(dir, "cache", hex.EncodeToString(sum[:])+".json")
}

func (c *BoundaryCache) Load(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxCheckpointBytes+1))
	if err != nil || len(raw) > MaxCheckpointBytes {
		return false
	}
	var b boundaryStore
	if json.Unmarshal(raw, &b) != nil || b.Format != "cum-source-map-2" || len(b.Entries) > maxHistoryCandidates || len(b.Archives) > maxHistoryCandidates || len(b.Equivalences) > maxHistoryCandidates || b.Integrity != b.digest() {
		return false
	}
	entries := make(map[boundaryKey]boundaryEntry, len(b.Entries))
	for _, v := range b.Entries {
		if v.Path == "" || len(v.Path) > 32768 || v.Start < 0 || v.End <= v.Start || v.Declared <= 0 || v.Resolved <= 0 || (!compressedPath(v.Path) && v.Resolved > v.Stamp.Size) || v.Resolved > maxArchiveBytes && compressedPath(v.Path) {
			return false
		}
		key := boundaryKey{v.Path, v.Header, v.Start, v.End, v.Declared}
		if _, exists := entries[key]; exists {
			return false
		}
		entries[key] = boundaryEntry{v.Stamp, v.Resolved}
	}
	for path, a := range b.Archives {
		if len(path) > 32768 || !compressedPath(path) || a.Size < 0 || a.Size > maxArchiveBytes || a.Meta.Type != "session_meta" {
			return false
		}
	}
	c.entries, c.dirty = entries, false
	c.archives, c.equivalences = b.Archives, b.Equivalences
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

func (c *BoundaryCache) Save(path string) error {
	if !c.dirty {
		return nil
	}
	b := boundaryStore{Format: "cum-source-map-2", Archives: c.archives, Equivalences: c.equivalences}
	for k, v := range c.entries {
		b.Entries = append(b.Entries, boundaryRecord{k.path, k.header, k.start, k.end, k.declared, v.bytes, v.stamp})
	}
	b.Integrity = b.digest()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(raw) > MaxCheckpointBytes {
		return errors.New("source mapping exceeds cache budget")
	}
	if err := storage.WriteAtomic(path, raw); err != nil {
		return err
	}
	c.dirty = false
	PruneCache(filepath.Dir(path), path)
	return nil
}

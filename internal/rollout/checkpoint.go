package rollout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/storage"
)

type checkpoint struct {
	Format       string
	Version      int
	Integrity    string
	Offset       int64
	Size         int64
	Dropping     bool
	State        *metrics.State
	HistoryKey   string
	NextOrdinal  int64
	OrdinalKnown bool
	Seen         []uint64
	Parts        []prefixProof
	SeenIndex    *indexCheckpoint
	Plan         *History
}

const MaxCheckpointBytes = 2 << 20

const checkpointVersion = 6
const checkpointFormat = "cum-history-prefixes"

// Bind the aggregate values and resume metadata as well as the source bytes.
// This detects valid-JSON corruption; it is not an authentication mechanism.
func (c checkpoint) integrity() (string, error) {
	c.Integrity = ""
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (r *Reader) SaveCheckpoint(path string) error {
	return r.SaveCheckpointContext(context.Background(), path)
}

func (r *Reader) SaveCheckpointContext(ctx context.Context, path string) error {
	return r.saveCheckpoint(ctx, path, 0)
}

func (r *Reader) saveCheckpoint(ctx context.Context, path string, prefix int) error {
	defer clearDecoders()
	if r.closed {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.info == nil {
		return nil
	}
	if !r.State.ValidateBudget() {
		return errors.New("state exceeds cache budget")
	}
	if r.failure != nil {
		return r.failure
	}
	f, e := r.openSource(ctx)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	matches, e := r.matchesContext(ctx, f, info)
	if e != nil {
		return e
	}
	if !matches {
		return r.sourceFailure(ErrHistoryChanged)
	}
	offset := r.Offset - int64(len(r.pending))
	c := checkpoint{Format: checkpointFormat, Version: checkpointVersion, Offset: offset, Size: r.info.Size(), Dropping: r.dropping, State: r.State,
		HistoryKey: r.historyKey(), NextOrdinal: r.nextOrdinal, OrdinalKnown: r.ordinalKnown, Seen: r.seen, Plan: r.history}
	c.Parts = append([]prefixProof(nil), r.proofs...)
	if prefix > 0 {
		if r.history == nil || prefix >= len(r.history.Segments) || r.Boundary() != prefix || r.dropping || len(r.pending) > 0 {
			return errors.New("unsettled checkpoint boundary")
		}
		h := History{Segments: append([]Segment(nil), r.history.Segments[:prefix]...)}
		h.Head = h.Segments[prefix-1].Path
		c.Plan, c.HistoryKey, c.Size = &h, h.key(), offset
		c.Parts = c.Parts[:prefix]
	}
	for i := range c.Parts {
		c.Parts[i].Digest = c.Parts[i].sum()
		c.Parts[i].hasher = nil
	}
	c.SeenIndex, e = saveIndex(ctx, indexCachePath(path, "seen"), r.seenIndex)
	if e != nil {
		return e
	}
	c.Integrity, e = c.integrity()
	if e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	if len(b) > MaxCheckpointBytes {
		return errors.New("checkpoint exceeds cache budget")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.WriteAtomic(path, b); err != nil {
		return err
	}
	PruneCache(filepath.Dir(path), path)
	return nil
}

func (r *Reader) RestoreCheckpoint(path string) bool {
	return r.RestoreCheckpointContext(context.Background(), path)
}

func (r *Reader) RestoreCheckpointContext(ctx context.Context, path string) bool {
	return r.restoreCheckpoint(ctx, path, false)
}

func (r *Reader) restoreCheckpoint(ctx context.Context, path string, prefix bool) bool {
	defer clearDecoders()
	if r.closed || ctx.Err() != nil {
		return false
	}
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxCheckpointBytes+1))
	f.Close()
	if e != nil || len(b) > MaxCheckpointBytes {
		return false
	}
	var c checkpoint
	if json.Unmarshal(b, &c) != nil || c.Format != checkpointFormat || c.Version != checkpointVersion || c.State == nil || c.State.Turns == nil || c.Offset < 0 || c.Size < c.Offset || !c.State.ValidateBudget() ||
		c.NextOrdinal < 0 || len(c.Seen) != 0 && len(c.Seen) != seenWords {
		return false
	}
	if digest, err := c.integrity(); err != nil || digest != c.Integrity {
		return false
	}
	if prefix {
		if !r.acceptsPrefix(c) {
			return false
		}
	} else if c.HistoryKey != r.historyKey() {
		return false
	}
	if c.Plan != nil && c.Plan.key() != c.HistoryKey {
		return false
	}
	if r.history != nil {
		if c.Plan == nil {
			return false
		}
		if c.State.StepCount > 0 && len(c.Seen) != seenWords {
			return false
		}
		if r.history.Segments[0].StartOrdinal >= 0 && c.Offset > 0 && !c.OrdinalKnown {
			return false
		}
	}
	previous := r.proofs
	restored := false
	defer func() {
		if !restored {
			r.proofs = previous
		}
	}()
	// An identical cached plan already binds header and fixed-boundary checks.
	// Install only structurally identical proofs before opening sources, so
	// unchanged compressed ancestors need no decode just to restore a cache.
	if r.history != nil && c.Plan != nil && len(c.Plan.Segments) == len(c.Parts) {
		r.proofs = make([]prefixProof, len(r.history.Segments))
		for i, p := range c.Parts {
			if i >= len(r.history.Segments) {
				return false
			}
			old, now := c.Plan.Segments[i], r.history.Segments[i]
			if p.Path == now.Path && p.Limit == now.Bytes && old.Header == now.Header && old.StartOrdinal == now.StartOrdinal && old.EndOrdinal == now.EndOrdinal && old.LogicalID == now.LogicalID {
				r.proofs[i] = p
			}
		}
	}
	source, e := r.openSource(ctx)
	if e != nil {
		return false
	}
	defer source.Close()
	info, e := source.Stat()
	if e != nil || info.Size() < c.Offset {
		return false
	}
	parts := r.sourceParts(source, info)
	if len(c.Parts) > len(parts) || !prefix && len(c.Parts) != len(parts) || len(c.Parts) > maxHistorySegments {
		return false
	}
	var bytes int64
	for i, p := range c.Parts {
		limit := int64(-1)
		if r.history != nil {
			limit = r.history.Segments[i].Bytes
		}
		if (!prefix && p.Path != parts[i].path) || (prefix && !sameRepresentationPath(p.Path, parts[i].path)) || !prefix && p.Limit != limit || p.Bytes < 0 || p.Bytes > c.Offset-bytes || p.Bytes > parts[i].length {
			return false
		}
		bytes += p.Bytes
		if bytes < c.Offset && limit >= 0 && p.Bytes != limit {
			return false
		}
		c.Parts[i].Limit = limit
		c.Parts[i].Path = parts[i].path
	}
	if bytes != c.Offset {
		return false
	}
	r.startProofs(parts)
	copy(r.proofs, c.Parts)
	if r.verifyProofs(ctx, parts) != nil {
		return false
	}
	if len(c.State.Turns) > 32 || !c.State.Usage.Valid() {
		return false
	}
	if len(c.State.Order) > 32 || c.State.ModelMS < 0 || c.State.ToolMS < 0 || c.State.TTFTSum < 0 || c.State.TTFTCount < 0 {
		return false
	}
	if c.State.TurnCount < 0 || c.State.StepCount < c.State.TurnCount {
		return false
	}
	if c.State.MissingRecords < 0 || c.State.MissingRecords > c.NextOrdinal || c.State.MissingRecords > 0 &&
		(!c.OrdinalKnown || c.State.HistoryComplete || c.State.TimingComplete || !c.State.CountsIncomplete) {
		return false
	}
	if c.State.DecodeTokens < 0 || c.State.DecodeMS < 0 || c.State.DecodeSamples < 0 || c.State.DecodeSkipped < 0 ||
		(c.State.DecodeSamples == 0) != (c.State.DecodeMS == 0) {
		return false
	}
	for id, t := range c.State.Turns {
		if len(id) > 256 || t == nil || len(t.Model) > 8192 || len(t.Tools) > 8192 {
			return false
		}
		if t.LastResponseMS < 0 || t.PendingDecode.Start < 0 || t.PendingDecode.End < t.PendingDecode.Start {
			return false
		}
	}
	if c.OrdinalKnown && c.Offset > 0 && !c.Dropping {
		line, err := lineEndingAt(source, c.Offset)
		var record struct {
			Ordinal *int64 `json:"ordinal"`
		}
		if err != nil || json.Unmarshal(line, &record) != nil || record.Ordinal == nil || *record.Ordinal != c.NextOrdinal-1 {
			return false
		}
	}
	seenIndex := restoreIndex(ctx, indexCachePath(path, "seen"), c.SeenIndex)
	if ctx.Err() != nil || r.verifyProofs(ctx, parts) != nil {
		if seenIndex != nil {
			seenIndex.close()
		}
		return false
	}
	r.State = c.State
	r.closeChunk()
	r.closePairs()
	r.closeSeenIndex()
	r.seenIndex = seenIndex
	r.Offset = c.Offset
	r.info = info
	r.pending = nil
	r.dropping = c.Dropping
	r.nextOrdinal, r.ordinalKnown, r.seen, r.failure = c.NextOrdinal, c.OrdinalKnown, c.Seen, nil
	restored = true
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return true
}

func (r *Reader) historyKey() string {
	if r.sourceID == "" {
		if r.history != nil {
			r.sourceID = r.history.key()
		} else {
			r.sourceID = (History{Head: r.Path}).key()
		}
	}
	return r.sourceID
}

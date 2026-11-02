package rollout

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/source"
)

var (
	ErrHomeUnavailable       = errors.New("Codex data folder unavailable")
	ErrLogNotFound           = errors.New("current chat log not found")
	ErrLogAmbiguous          = errors.New("multiple history heads match this chat")
	ErrLogIdentityUnverified = errors.New("chat log identity unverified")
	ErrLogPending            = errors.New("chat log header incomplete")
	ErrLogSearchIncomplete   = errors.New("chat log search incomplete")
	ErrLogUnreadable         = errors.New("chat log unreadable")
	ErrLogPathUnsupported    = errors.New("chat log path is not Windows-readable")
	ErrHistoryMissing        = errors.New("referenced history missing")
	ErrHistoryBoundary       = errors.New("history reference boundary invalid")
	ErrHistoryCycle          = errors.New("history reference cycle or depth exceeded")
	ErrHistoryChanged        = errors.New("history metadata changed")
	ErrHistoryOrdinal        = errors.New("history record sequence invalid")
	ErrAccountingConflict    = metrics.ErrAccountingConflict
	ErrSourceBusy            = errors.New("history source still changing")
	ErrBranchUnverified      = errors.New("Desktop current history branch unavailable")
)

const maxHistorySegments = 64
const maxHistoryCandidates = 128

type historyBase struct {
	RolloutID string `json:"thread_id"` // This field addresses a physical rollout, not a logical chat.
	Ordinal   *int64 `json:"end_ordinal_exclusive"`
	Bytes     *int64 `json:"end_byte_offset"`
}

type logMetadata struct {
	Ordinal *int64 `json:"ordinal"`
	Type    string `json:"type"`
	Payload struct {
		ID   string       `json:"id"`
		Base *historyBase `json:"history_base"`
		Mode string       `json:"history_mode"`
	} `json:"payload"`
}

type logNode struct {
	path, physical, logical string
	meta                    logMetadata
	headerHash              [32]byte
	archive                 *archiveInfo
	alternate               *logNode
}

// Header IDs establish ownership; history_base edges establish order. A
// physical rollout ID from the canonical filename only resolves an exact edge.
type Locator struct {
	Home       string
	Boundaries *BoundaryCache
	// ActivePath comes from the selected Desktop owner's revision-checked state.
	// It selects the exact branch, including an empty replacement root.
	ActivePath string
}

func rolloutName(name string) (logical, physical string, ok bool) {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".zst")
	if !strings.HasSuffix(name, ".jsonl") || !strings.HasPrefix(name, "rollout-") {
		return
	}
	core := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	if len(core) < 20 || core[19] != '-' {
		return
	}
	// Syntax only: this timestamp is never used to select or order history.
	if _, err := time.Parse("2006-01-02t15-04-05", core[:19]); err != nil {
		return
	}
	logical, physical, ok = strings.Cut(core[20:], "_")
	if !ok {
		physical = logical
	}
	ok = ipc.ValidThreadID(logical) && ipc.ValidThreadID(physical)
	return
}

func readHeader(ctx context.Context, path string) (logMetadata, [32]byte, error) {
	var meta logMetadata
	var sum [32]byte
	if err := ctx.Err(); err != nil {
		return meta, sum, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return meta, sum, ErrLogNotFound
		}
		return meta, sum, ErrLogUnreadable
	}
	if !info.Mode().IsRegular() {
		return meta, sum, ErrLogUnreadable
	}
	f, err := os.Open(path)
	if err != nil {
		return meta, sum, ErrLogUnreadable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return meta, sum, ErrLogUnreadable
	}
	meta, sum, err = decodeHeader(ctx, io.LimitReader(f, MaxRecordBytes+1))
	if err != nil {
		return meta, sum, err
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, current) {
		return meta, sum, ErrLogUnreadable
	}
	return meta, sum, ctx.Err()
}

func decodeHeader(ctx context.Context, source io.Reader) (logMetadata, [32]byte, error) {
	var meta logMetadata
	var sum [32]byte
	r := bufio.NewReaderSize(source, 4096)
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return meta, sum, err
		}
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > MaxRecordBytes {
			return meta, sum, ErrLogIdentityUnverified
		}
		line = appendRecord(line, part)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			return meta, sum, ErrLogPending
		}
		if err != nil {
			return meta, sum, ErrLogUnreadable
		}
		break
	}
	if json.Unmarshal(line, &meta) != nil || meta.Type != "session_meta" || !ipc.ValidThreadID(meta.Payload.ID) {
		return meta, sum, ErrLogIdentityUnverified
	}
	if meta.Ordinal != nil && *meta.Ordinal < 0 {
		return meta, sum, ErrHistoryOrdinal
	}
	if meta.Payload.Mode == "paginated" && meta.Ordinal == nil {
		return meta, sum, ErrHistoryOrdinal
	}
	if b := meta.Payload.Base; b != nil {
		if !ipc.ValidThreadID(b.RolloutID) || b.Bytes == nil || b.Ordinal == nil || *b.Bytes < 0 || *b.Ordinal < 0 || meta.Ordinal == nil || *meta.Ordinal != *b.Ordinal {
			return meta, sum, ErrHistoryBoundary
		}
	}
	return meta, sha256.Sum256(line), ctx.Err()
}

func nodeAt(ctx context.Context, path string) (logNode, error) {
	return nodeAtCached(ctx, path, nil)
}

func nodeAtCached(ctx context.Context, path string, cache *BoundaryCache) (logNode, error) {
	if compressedPath(path) {
		if info, e := os.Lstat(plainArchivePath(path)); e == nil && info.Mode().IsRegular() {
			path = plainArchivePath(path)
		}
	}
	if !compressedPath(path) {
		if _, e := os.Lstat(path); errors.Is(e, os.ErrNotExist) {
			if info, e := os.Lstat(path + ".zst"); e == nil && info.Mode().IsRegular() {
				path += ".zst"
			}
		}
	}
	nameID, physical, ok := rolloutName(filepath.Base(path))
	if !ok {
		return logNode{}, ErrLogIdentityUnverified
	}
	var meta logMetadata
	var hash [32]byte
	var err error
	var archive *archiveInfo
	if compressedPath(path) {
		a, e := cache.archiveHeader(ctx, path)
		meta, hash, err = a.Meta, a.Header, e
		archive = &a
	} else {
		meta, hash, err = readHeader(ctx, path)
	}
	if err != nil {
		return logNode{}, err
	}
	if !strings.EqualFold(meta.Payload.ID, nameID) {
		return logNode{}, ErrChatMismatch
	}
	return logNode{path: path, physical: physical, logical: strings.ToLower(meta.Payload.ID), meta: meta, headerHash: hash, archive: archive}, nil
}

func (l Locator) Resolve(ctx context.Context, id string) (History, error) {
	defer clearDecoders()
	if l.Boundaries == nil {
		l.Boundaries = &BoundaryCache{}
	}
	if err := ctx.Err(); err != nil {
		return History{}, err
	}
	if !ipc.ValidThreadID(id) {
		return History{}, ErrLogIdentityUnverified
	}
	id = strings.ToLower(id)
	nodes := make(map[string]logNode)
	add := func(n logNode) error {
		if old, exists := nodes[n.physical]; exists {
			if old.path == n.path {
				return nil
			}
			a, ea := os.Stat(old.path)
			b, eb := os.Stat(n.path)
			if ea == nil && eb == nil && os.SameFile(a, b) {
				return nil
			}
			if (old.archive == nil) != (n.archive == nil) && old.alternate == nil && n.alternate == nil {
				if old.archive != nil {
					old, n = n, old
				}
				if !sameMetadata(old.meta, n.meta) {
					return ErrLogAmbiguous
				}
				old.alternate = &n
				nodes[old.physical] = old
				return nil
			}
			return ErrLogAmbiguous
		}
		if len(nodes) >= maxHistoryCandidates {
			return ErrHistoryCycle
		}
		nodes[n.physical] = n
		return nil
	}
	if l.ActivePath == "" {
		return History{}, ErrBranchUnverified
	}
	if !filepath.IsAbs(l.ActivePath) {
		return History{}, ErrLogPathUnsupported
	}
	head, err := nodeAtCached(ctx, l.ActivePath, l.Boundaries)
	if err != nil {
		return History{}, err
	}
	if head.logical != id {
		return History{}, ErrChatMismatch
	}
	if err := add(head); err != nil {
		return History{}, err
	}
	var catalog map[string][]string
	getParent := func(physical string) (logNode, error) {
		physical = strings.ToLower(physical)
		if n, ok := nodes[physical]; ok {
			return n, nil
		}
		if catalog == nil {
			catalog = make(map[string][]string)
			bytes := 0
			err := l.walk(ctx, func(path, _, rollID string) error {
				bytes += len(path) + len(rollID) + 64
				if bytes > 32<<20 {
					return ErrLogSearchIncomplete
				}
				catalog[rollID] = append(catalog[rollID], path)
				return nil
			})
			if err != nil {
				return logNode{}, err
			}
		}
		paths := catalog[physical]
		if len(paths) == 0 {
			return logNode{}, ErrHistoryMissing
		}
		for _, path := range paths {
			n, err := nodeAtCached(ctx, path, l.Boundaries)
			if err != nil {
				return logNode{}, err
			}
			if err := add(n); err != nil {
				return logNode{}, err
			}
		}
		return nodes[physical], nil
	}
	var reversed []Segment
	seen := make(map[string]bool)
	n, limit, endOrdinal := head, int64(-1), int64(-1)
	for {
		if err := ctx.Err(); err != nil {
			return History{}, err
		}
		if seen[n.physical] || len(reversed) >= maxHistorySegments {
			return History{}, ErrHistoryCycle
		}
		seen[n.physical] = true
		if n.archive != nil {
			a, e := l.Boundaries.archive(ctx, n.path)
			if e != nil {
				return History{}, e
			}
			n.archive = &a
		}
		start := int64(-1)
		if n.meta.Ordinal != nil {
			start = *n.meta.Ordinal
		}
		s := Segment{Path: n.path, Bytes: limit, StartOrdinal: start, EndOrdinal: endOrdinal, Header: n.headerHash, LogicalID: n.logical, Archive: n.archive}
		if limit >= 0 {
			s, err = l.Boundaries.resolve(ctx, s)
			if err != nil {
				return History{}, err
			}
		}
		if n.alternate != nil {
			a := *n.alternate.archive
			a.Size = 0
			s.AlternatePath, s.Alternate = n.alternate.path, &a
		}
		reversed = append(reversed, s)
		b := n.meta.Payload.Base
		if b == nil {
			if start > 0 || len(reversed) > 1 && start != 0 {
				return History{}, ErrHistoryMissing
			}
			break
		}
		limit, endOrdinal = *b.Bytes, *b.Ordinal
		n, err = getParent(b.RolloutID)
		if err != nil {
			return History{}, err
		}
	}
	segments := make([]Segment, len(reversed))
	for i := range reversed {
		segments[len(reversed)-1-i] = reversed[i]
	}
	return History{Head: head.path, Segments: segments, sources: l.Boundaries}, nil
}

func (l Locator) walk(ctx context.Context, visit func(path, logical, physical string) error) error {
	info, err := os.Stat(l.Home)
	if err != nil || !info.IsDir() {
		return ErrHomeUnavailable
	}
	for _, dir := range []string{"sessions", "archived_sessions"} {
		root := source.JoinPath(l.Home, dir)
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return ErrLogSearchIncomplete
		}
		if err := walkLogNames(ctx, root, 0, visit); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func walkLogNames(ctx context.Context, dir string, depth int, visit func(path, logical, physical string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 16 {
		return ErrLogSearchIncomplete
	}
	f, err := os.Open(dir)
	if err != nil {
		return ErrLogSearchIncomplete
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(128)
		if err != nil && err != io.EOF {
			return ErrLogSearchIncomplete
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if err := walkLogNames(ctx, source.JoinPath(dir, entry.Name()), depth+1, visit); err != nil {
					return err
				}
			} else if entry.Type().IsRegular() && (strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") || compressedPath(entry.Name())) {
				if compressedPath(entry.Name()) {
					if info, e := os.Lstat(source.JoinPath(dir, plainArchivePath(entry.Name()))); e == nil && info.Mode().IsRegular() {
						continue
					}
				}
				if logical, physical, ok := rolloutName(entry.Name()); ok {
					if err := visit(source.JoinPath(dir, entry.Name()), logical, physical); err != nil {
						return err
					}
				}
			}
		}
		if err == io.EOF {
			return ctx.Err()
		}
	}
}

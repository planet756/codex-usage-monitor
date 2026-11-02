package rollout

import (
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/source"
	"context"
	"errors"
	"strings"
	"time"
)

var ErrChatMismatch = errors.New("chat ID mismatch")

// Tracked re-resolves a verified chat, never guesses a recent or nearby file.
// A moved log gets its own reader/cache; the caller replaces its directory watch.
type Tracked struct {
	ID, CacheDir   string
	Resolve        func() (History, error)
	Reader         *Reader
	CachePath      string
	Incremental    bool
	Timing         LoadTiming
	Started        time.Time
	savedOffset    int64
	lastPrefix     int64
	firstCounts    bool
	verifyBase     time.Duration
	nextResolve    time.Time
	dirty, healthy bool
	resolutionErr  error
}

func (t *Tracked) resolve(ctx context.Context, now time.Time) (moved bool, err error) {
	t.nextResolve = now.Add(30 * time.Second)
	defer func() {
		t.resolutionErr = err
		if err != nil {
			t.nextResolve = now.Add(2 * time.Second)
		}
	}()
	if t.Started.IsZero() {
		t.Started = time.Now()
	}
	started := time.Now()
	history, err := t.Resolve()
	locateMS := time.Since(started).Milliseconds()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return false, err
	}
	path := history.Head
	if path == "" {
		return false, errors.New("chat log path unavailable")
	}
	if t.Reader != nil && t.Reader.historyKey() == history.key() {
		return false, nil
	}
	_ = t.SaveContext(ctx)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if t.Reader != nil {
		t.Reader.Close()
		t.Started = started
	}
	t.Reader = NewHistory(history)
	t.Timing = LoadTiming{Started: t.Started, LocateMS: locateMS, Segments: len(history.Segments)}
	t.verifyBase = 0
	for _, s := range history.Segments {
		if s.Archive != nil {
			t.Timing.Archives++
		}
	}
	t.CachePath = checkpointPath(t.CacheDir, path)
	started = time.Now()
	t.Timing.Mode, _ = t.Reader.RestoreBest(ctx, t.CacheDir)
	t.Timing.RestoreMS = max(0, (time.Since(started) - t.Reader.VerifyTime).Milliseconds())
	t.Timing.ResumedBytes = t.Reader.Offset
	t.savedOffset = t.Reader.Offset
	t.lastPrefix = t.Reader.Offset
	t.firstCounts = false
	t.dirty, t.healthy = false, false
	return true, nil
}

func (t *Tracked) Update(ctx context.Context, now time.Time) (changed, relocated bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if !now.Before(t.nextResolve) {
		relocated, err = t.resolve(ctx, now)
		if t.Reader == nil {
			return
		}
		// A busy catalog does not invalidate a still-readable verified log.
	}
	if t.Reader == nil {
		return false, false, t.resolutionErr
	}
	// An ambiguity or identity failure must not disappear on the next directory
	// notification just because the previous reader still exists.
	if identityBlocked(t.resolutionErr) {
		t.healthy = false
		return false, relocated, t.resolutionErr
	}
	if err = ctx.Err(); err != nil {
		t.healthy = false
		return
	}
	read := func() (bool, error) {
		started, verifyBefore, offset := time.Now(), t.Reader.VerifyTime, t.Reader.Offset
		generation := t.Reader.rebuilds
		var changed bool
		var err error
		if t.Incremental {
			changed, err = t.Reader.UpdateChunk(ctx)
		} else {
			changed, err = t.Reader.Update(ctx)
		}
		if t.Reader.rebuilds != generation {
			t.Started = started
			t.Timing = LoadTiming{Started: started, Mode: "rebuild", Segments: t.Timing.Segments, Archives: t.Timing.Archives}
			t.verifyBase = verifyBefore
			t.firstCounts = false
			t.lastPrefix = 0
			offset = 0
		}
		if t.Timing.ReadyMS == 0 {
			t.Timing.ReadMS += max(0, (time.Since(started) - (t.Reader.VerifyTime - verifyBefore)).Milliseconds())
			t.Timing.ReadBytes += max(0, t.Reader.Offset-offset)
			t.Timing.VerifyMS = (t.Reader.VerifyTime - t.verifyBase).Milliseconds()
			if t.Reader.info != nil {
				t.Timing.HistoryBytes = t.Reader.info.Size()
			}
		}
		return changed, err
	}
	changed, err = read()
	if err != nil && !errors.Is(err, ErrSourceBusy) && ctx.Err() == nil {
		moved, resolveErr := t.resolve(ctx, now)
		relocated = relocated || moved
		if identityBlocked(resolveErr) {
			t.healthy = false
			return false, relocated, resolveErr
		}
		if resolveErr == nil && moved {
			changed, err = read()
		}
		if resolveErr != nil && !moved {
			err = resolveErr
		}
	}
	if err != nil {
		t.healthy = false
		return
	}
	if t.Reader.State.ThreadID == "" {
		t.healthy, t.dirty = false, false
		return false, relocated, ErrLogIdentityUnverified
	}
	if !t.Reader.Loading() && !strings.EqualFold(t.Reader.State.ThreadID, t.ID) {
		t.healthy, t.dirty = false, false
		return false, relocated, ErrChatMismatch
	}
	t.healthy = true
	t.dirty = t.dirty || changed || t.Reader.Offset != t.savedOffset
	if !t.firstCounts && (t.Reader.State.StepCount > 0 || !t.Reader.Loading()) {
		t.Timing.FirstCountsMS = max(1, time.Since(t.Started).Milliseconds())
		t.firstCounts = true
	}
	if !t.Reader.Loading() && t.Timing.ReadyMS == 0 {
		t.Timing.ReadyMS = max(1, time.Since(t.Started).Milliseconds())
	}
	if t.Incremental && t.Reader.history != nil {
		n := t.Reader.Boundary()
		if n > 0 && (t.Reader.Offset-t.lastPrefix >= 128<<20 || n == len(t.Reader.history.Segments)-1) {
			before := t.Reader.VerifyTime
			at := time.Now()
			e := t.Reader.SaveBoundary(ctx, t.CacheDir)
			t.Timing.SaveMS += max(0, (time.Since(at) - (t.Reader.VerifyTime - before)).Milliseconds())
			t.Timing.VerifyMS = (t.Reader.VerifyTime - t.verifyBase).Milliseconds()
			if errors.Is(e, ErrHistoryChanged) {
				t.healthy = false
				return false, relocated, e
			}
			if e == nil {
				t.lastPrefix = t.Reader.Offset
			}
		}
	}
	return changed, relocated, nil
}

func (t *Tracked) Snapshot() metrics.Snapshot {
	if t.Reader == nil {
		return metrics.Snapshot{}
	}
	s := t.Reader.State.Snapshot()
	s.ThreadID = t.ID
	if t.Reader.Loading() {
		if s.Steps == 0 {
			s.HasCounts = false
		}
		s.CountsComplete = false
		s.HistoryComplete = false
		s.TimingComplete = false
		s.HasUsage = false
		s.Uncached = nil
		s.CachePercent = nil
		s.TPS = nil
		s.AverageTTFT = nil
		s.HasTiming = false
	}
	return s
}

func identityBlocked(err error) bool {
	return errors.Is(err, source.ErrHostUnsupported) || errors.Is(err, ErrLogPathUnsupported) || errors.Is(err, ErrBranchUnverified) || errors.Is(err, ErrArchiveInvalid) || errors.Is(err, ErrAccountingConflict) || errors.Is(err, ErrLogAmbiguous) || errors.Is(err, ErrChatMismatch) ||
		errors.Is(err, ErrLogIdentityUnverified) || errors.Is(err, ErrLogPending) ||
		errors.Is(err, ErrLogSearchIncomplete) || errors.Is(err, ErrLogUnreadable) ||
		errors.Is(err, ErrHistoryMissing) || errors.Is(err, ErrHistoryBoundary) ||
		errors.Is(err, ErrHistoryCycle) || errors.Is(err, ErrHistoryChanged) || errors.Is(err, ErrHistoryOrdinal)
}

func (t *Tracked) Save() error {
	return t.SaveContext(context.Background())
}

func (t *Tracked) SaveContext(ctx context.Context) error {
	if !t.dirty || !t.healthy || t.Reader == nil {
		return nil
	}
	if err := t.Reader.SaveCheckpointContext(ctx, t.CachePath); err != nil {
		if errors.Is(err, ErrHistoryChanged) {
			t.healthy, t.dirty = false, false
		}
		return err
	}
	t.dirty = false
	t.savedOffset = t.Reader.Offset
	return nil
}

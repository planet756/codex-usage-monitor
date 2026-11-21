package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/presentation"
	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

type Update struct {
	ID, Message         string
	Host                string
	Generation          uint64
	ObservedAt          time.Time
	Snapshot            metrics.Snapshot
	Loading             bool
	Connected           bool
	Live                live.View
	RecoveredBoundaries int
	Timing              rollout.LoadTiming
	BranchVerified      bool
	SourceKind          string
	SourceMapped        bool
}

type sourceSelection struct {
	ownerPath, indexAtOwner, lastIndex, replacedOwner string
}

func (s *sourceSelection) choose(indexed string, err error, hint string, samePath func(string, string) bool) (string, error) {
	if err == nil {
		s.lastIndex = indexed
	} else if !errors.Is(err, rollout.ErrStateIndexUnavailable) && !errors.Is(err, rollout.ErrLogNotFound) {
		return indexed, err
	}
	if hint != "" && hint != s.ownerPath {
		s.replacedOwner = s.ownerPath
		s.ownerPath, s.indexAtOwner = hint, s.lastIndex
	}
	if hint == "" && s.ownerPath != "" && err == nil {
		if samePath(indexed, s.ownerPath) {
			return indexed, nil
		}
		if s.indexAtOwner == "" {
			s.indexAtOwner = indexed
		}
		// Until the index advances, a dropped connection must not restore
		// history the verified owner has already superseded.
		if !samePath(indexed, s.indexAtOwner) && !samePath(indexed, s.replacedOwner) {
			s.ownerPath, s.indexAtOwner, s.replacedOwner = "", "", ""
		}
	}
	if s.ownerPath != "" {
		return s.ownerPath, nil
	}
	return indexed, err
}

func runReader(ctx context.Context, home, host, id, cacheDir string, started time.Time, hints <-chan string, selected *sourceSelection, publish func(Update), deps Dependencies) {
	boundaries := &rollout.BoundaryCache{}
	boundaryPath := rollout.BoundaryCachePath(cacheDir, id)
	boundaries.Load(boundaryPath)
	var selectedSource source.Plan
	var path, hint string
	var indexErr error
	samePath := func(left, right string) bool {
		if left == right {
			return true
		}
		options := source.Options{Home: home, Host: host}
		a, errA := deps.Resolve(left, options)
		b, errB := deps.Resolve(right, options)
		if errA != nil || errB != nil {
			return false
		}
		if rollout.SameRepresentationPath(a.Path, b.Path) {
			return true
		}
		x, errX := os.Stat(a.Path)
		y, errY := os.Stat(b.Path)
		return errX == nil && errY == nil && os.SameFile(x, y)
	}
	refreshIndex := func() bool {
		indexed, err := deps.IndexedPath(ctx, home, host, id)
		next, err := selected.choose(indexed, err, hint, samePath)
		changed := path != next || !errors.Is(indexErr, err) || !errors.Is(err, indexErr)
		path, indexErr = next, err
		return changed
	}
	refreshIndex()
	indexTicker := time.NewTicker(2 * time.Second)
	defer indexTicker.Stop()
	tracked := rollout.Tracked{ID: id, CacheDir: cacheDir, Incremental: true, Started: started, Resolve: func() (rollout.History, error) {
		if indexErr != nil {
			return rollout.History{}, indexErr
		}
		h, p, err := ResolveActiveThread(ctx, home, host, id, path, boundaries, deps.Resolve)
		selectedSource = p
		return h, err
	}}
	defer func() {
		if tracked.Reader != nil {
			tracked.Reader.Close()
		}
	}()
	fallback := time.NewTicker(15 * time.Second)
	defer fallback.Stop()
	changes := make(chan struct{}, 1)
	var watchDone chan error
	var cancelWatch context.CancelFunc
	var watching bool
	var watchedPath string
	var lastWatch time.Time
	stopWatch := func() {
		if cancelWatch != nil {
			cancelWatch()
			if watching {
				<-watchDone
			}
			cancelWatch = nil
		}
		watching = false
	}
	defer stopWatch()
	startWatch := func() {
		if tracked.Reader == nil {
			return
		}
		stopWatch()
		// Capture immutable values; an old notification cannot stop a new watcher.
		watchedPath = tracked.Reader.Path
		child, cancel := context.WithCancel(ctx)
		cancelWatch = cancel
		done := make(chan error, 1)
		watchDone = done
		path := watchedPath
		go func() { done <- deps.Watch(child, source.DirPath(path), filepath.Base(path), changes) }()
		watching = true
		lastWatch = time.Now()
		fallback.Reset(15 * time.Second)
	}
	healthy, initial := false, true
	var verified Update
	update := func() {
		changed, moved, err := tracked.Update(ctx, time.Now())
		if ctx.Err() != nil {
			return
		}
		if moved || tracked.Reader != nil && watchedPath != tracked.Reader.Path {
			startWatch()
		}
		if err != nil {
			if errors.Is(err, rollout.ErrSourceBusy) {
				u := verified
				u.ID = id
				u.Message = presentation.LogProblem(err)
				publish(u)
			} else {
				publish(Update{ID: id, Message: presentation.LogProblem(err)})
			}
			healthy = false
			fallback.Reset(2 * time.Second)
			return
		}
		if changed || moved || initial || !healthy {
			verified = Update{ID: id, Snapshot: tracked.Snapshot(), Loading: tracked.Reader.Loading(), Timing: tracked.Timing, RecoveredBoundaries: tracked.Reader.RecoveredBoundaries(), BranchVerified: true, SourceKind: selectedSource.Kind, SourceMapped: selectedSource.Mapped}
			verified.ObservedAt = time.Now()
			publish(verified)
		}
		if !healthy && watching {
			fallback.Reset(15 * time.Second)
		}
		healthy, initial = true, false
		if ctx.Err() == nil {
			_ = boundaries.Save(boundaryPath)
		}
	}
	save := func(saveCtx context.Context) {
		if err := tracked.SaveContext(saveCtx); errors.Is(err, rollout.ErrHistoryChanged) && ctx.Err() == nil {
			publish(Update{ID: id, Message: presentation.LogProblem(err)})
			healthy = false
			fallback.Reset(2 * time.Second)
		}
	}
	recheckIndex := func() bool {
		previous := path
		if !refreshIndex() {
			return false
		}
		verified, healthy = Update{}, false
		if previous != "" && path != "" && path != previous {
			publish(Update{ID: id, Loading: true, Message: "Loading current chat…"})
		}
		tracked.RecheckSource()
		return true
	}
	applyHint := func(next string) bool {
		if next == hint {
			return false
		}
		hint = next
		return recheckIndex()
	}
	load := func() {
		for {
			// Large histories load in chunks. A new head must be observed
			// between chunks instead of waiting for the old scan to complete.
			select {
			case next := <-hints:
				applyHint(next)
			case <-indexTicker.C:
				recheckIndex()
			default:
			}
			update()
			if ctx.Err() != nil || !healthy || tracked.Reader == nil || !tracked.Reader.Loading() {
				return
			}
		}
	}
	load()
	if ctx.Err() == nil {
		save(ctx)
	}
	defer func() {
		final, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tracked.SaveContext(final)
	}()
	var debounce *time.Timer
	var ready <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	lastSave := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-hints:
			if applyHint(next) {
				load()
			}
		case <-indexTicker.C:
			if recheckIndex() {
				load()
			}
		case <-changes:
			if ready == nil {
				if debounce == nil {
					debounce = time.NewTimer(150 * time.Millisecond)
				} else {
					debounce.Reset(150 * time.Millisecond)
				}
				ready = debounce.C
			}
		case <-fallback.C:
			load()
			if !watching && time.Since(lastWatch) >= 30*time.Second {
				startWatch()
			}
		case <-watchDone:
			watching = false
			if cancelWatch != nil {
				cancelWatch()
				cancelWatch = nil
			}
			fallback.Reset(2 * time.Second)
		case <-ready:
			ready = nil
			load()
		}
		if time.Since(lastSave) >= 15*time.Second {
			save(ctx)
			lastSave = time.Now()
		}
	}
}

func (u Update) equal(v Update) bool {
	a, b := u.Snapshot, v.Snapshot
	u.Snapshot, v.Snapshot = metrics.Snapshot{}, metrics.Snapshot{}
	return u == v && a.Equal(b)
}

type readerRequest struct {
	id         string
	host       string
	hints      <-chan string
	generation uint64
	started    time.Time
}

// One manager joins the cancelled reader before starting the latest request.
// Rapid tab changes cannot leave a backlog of history scans/cache writers.
func readerManager(ctx context.Context, home, configPath string, jobs <-chan readerRequest, updates chan Update, deps Dependencies) {
	var cancel context.CancelFunc
	var done chan struct{}
	// Keep unresolved IPC/index disagreements across view switches. Each entry
	// has a single writer because stop joins the old reader before reusing it.
	selections := make(map[string]*sourceSelection)
	var readingID string
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			if selections[readingID].ownerPath == "" {
				delete(selections, readingID)
			}
		}
	}
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			stop()
			for {
				select {
				case job = <-jobs:
					continue
				default:
				}
				break
			}
			if ctx.Err() != nil {
				return
			}
			if job.id == "" {
				continue
			}
			child, c := context.WithCancel(ctx)
			cancel = c
			done = make(chan struct{})
			finished := done
			selected := selections[job.id]
			if selected == nil {
				if len(selections) >= 64 {
					clear(selections)
				}
				selected = &sourceSelection{}
				selections[job.id] = selected
			}
			readingID = job.id
			go func(job readerRequest) {
				defer close(finished)
				runReader(child, home, job.host, job.id, filepath.Dir(configPath), job.started, job.hints, selected, func(u Update) {
					u.Generation = job.generation
					select {
					case <-child.Done():
						return
					default:
					}
					select {
					case updates <- u:
					default:
						select {
						case <-updates:
						default:
						}
						select {
						case updates <- u:
						case <-child.Done():
						}
					}
				}, deps)
			}(job)
		}
	}
}

func Run(ctx context.Context, home, configPath string, views <-chan View, publish func(Update), deps Dependencies) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	routes := make(chan Route, 1)
	ipcViews := make(chan View, 1)
	ipcDone := make(chan struct{})
	go func() { defer close(ipcDone); Follow(ctx, ipcViews, routes, deps.Dial) }()
	updates := make(chan Update, 1)
	jobs := make(chan readerRequest, 1)
	managerDone := make(chan struct{})
	go func() { defer close(managerDone); readerManager(ctx, home, configPath, jobs, updates, deps) }()
	defer func() { cancel(); <-managerDone; <-ipcDone }()
	var active string
	var activeHost string
	var canRead bool
	var view View
	var hints chan string
	var generation uint64
	var latest, previous Update
	var published bool
	var selectedAt time.Time
	var loadStarted time.Time
	var delivered int64
	var last Route
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var ready <-chan time.Time
	emit := func() {
		u := latest
		if !u.Timing.Started.IsZero() && !u.Timing.Started.Equal(loadStarted) {
			loadStarted = u.Timing.Started
			delivered = 0
		}
		if u.Snapshot.HasCounts && delivered == 0 {
			start := loadStarted
			if start.IsZero() {
				start = selectedAt
			}
			delivered = max(1, time.Since(start).Milliseconds())
		}
		u.Timing.DeliveryMS = delivered
		u.ID, u.Host, u.Connected = active, activeHost, last.Connected
		u.Snapshot.ThreadID = active
		if last.epoch == view.epoch && last.ID == active && (last.Host == activeHost || activeHost == "" && last.Host == "local") && last.Live.ChatID == active {
			u.Live = last.Live
		}
		if !published || !u.equal(previous) {
			publish(u)
			previous = u
			published = true
		}
	}
	schedule := func() {
		if ready == nil {
			timer.Reset(500 * time.Millisecond)
			ready = timer.C
		}
	}
	restartReader := func() {
		generation++
		selectedAt = time.Now()
		loadStarted = time.Time{}
		delivered = 0
		latest = Update{ID: active, Host: activeHost}
		if active != "" && canRead {
			latest.Loading = true
			latest.Message = "Loading current chat…"
		} else if active != "" {
			latest.Message = "Current chat log is not on this computer"
		}
		hints = nil
		job := readerRequest{id: active, host: activeHost, generation: generation, started: selectedAt}
		if !canRead {
			job.id = ""
		} else {
			hints = make(chan string, 1)
			job.hints = hints
		}
		select {
		case jobs <- job:
		default:
			select {
			case <-jobs:
			default:
			}
			jobs <- job
		}
		timer.Stop()
		ready = nil
		emit()
	}
	selectHistory := func() {
		host := view.Host
		read := deps.IndexedPath != nil && view.ID != "" && (host == "local" || host == "")
		if active != view.ID || activeHost != host || canRead != read {
			active, activeHost, canRead = view.ID, host, read
			restartReader()
		}
		if active == "" {
			latest.Message = ipc.ViewMessage(view.Reason)
			emit()
		} else {
			schedule()
		}
		if hints != nil {
			path := ""
			if last.Connected && last.epoch == view.epoch && last.ID == active && last.Host == "local" && last.Live.ChatID == active && last.Live.Available && last.Live.OwnerVerified {
				path = last.Live.RolloutPath
			}
			select {
			case <-hints:
			default:
			}
			hints <- path
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case selected, ok := <-views:
			if !ok {
				views = nil
			}
			if !selected.ChatPage || selected.Reason != "verified" || !ipc.ValidThreadID(selected.ID) {
				selected.ID, selected.Host = "", ""
			}
			selected.epoch = view.epoch
			if selected.ID != view.ID || selected.Host != view.Host || selected.Window != view.Window {
				selected.epoch++
			}
			view = selected
			select {
			case ipcViews <- view:
			default:
				select {
				case <-ipcViews:
				default:
				}
				ipcViews <- view
			}
			selectHistory()
		case last = <-routes:
			selectHistory()
		case u := <-updates:
			if u.ID == active && u.Generation == generation {
				immediate := (!latest.Snapshot.HasCounts || !latest.Timing.Started.Equal(u.Timing.Started)) && u.Snapshot.HasCounts ||
					latest.Loading && !u.Loading || latest.BranchVerified && !u.BranchVerified
				latest = u
				if immediate {
					timer.Stop()
					ready = nil
					emit()
				} else {
					schedule()
				}
			}
		case <-ready:
			ready = nil
			emit()
		}
	}
}

package win

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/presentation"
	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

type dataUpdate struct {
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

func runReader(ctx context.Context, home, host, id, activePath, cacheDir string, started time.Time, publish func(dataUpdate)) {
	boundaries := &rollout.BoundaryCache{}
	boundaryPath := rollout.BoundaryCachePath(cacheDir, id)
	boundaries.Load(boundaryPath)
	var selectedSource source.Plan
	tracked := rollout.Tracked{ID: id, CacheDir: cacheDir, Incremental: true, Started: started, Resolve: func() (rollout.History, error) {
		h, p, err := resolveActiveThread(ctx, home, host, id, activePath, boundaries)
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
		go func() { done <- watchDirectory(child, source.DirPath(path), filepath.Base(path), changes) }()
		watching = true
		lastWatch = time.Now()
		fallback.Reset(15 * time.Second)
	}
	healthy, initial := false, true
	var verified dataUpdate
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
				publish(dataUpdate{ID: id, Message: presentation.LogProblem(err)})
			}
			healthy = false
			fallback.Reset(2 * time.Second)
			return
		}
		if changed || moved || initial || !healthy {
			verified = dataUpdate{ID: id, Snapshot: tracked.Snapshot(), Loading: tracked.Reader.Loading(), Timing: tracked.Timing, RecoveredBoundaries: tracked.Reader.RecoveredBoundaries(), BranchVerified: activePath != "", SourceKind: selectedSource.Kind, SourceMapped: selectedSource.Mapped}
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
			publish(dataUpdate{ID: id, Message: presentation.LogProblem(err)})
			healthy = false
			fallback.Reset(2 * time.Second)
		}
	}
	load := func() {
		for {
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

func (u dataUpdate) equal(v dataUpdate) bool {
	a, b := u.Snapshot, v.Snapshot
	u.Snapshot, v.Snapshot = metrics.Snapshot{}, metrics.Snapshot{}
	return u == v && a.Equal(b)
}

type readerRequest struct {
	id         string
	host       string
	path       string
	generation uint64
	started    time.Time
}

// One manager joins the cancelled reader before starting the latest request.
// Rapid tab changes cannot leave a backlog of history scans/cache writers.
func readerManager(ctx context.Context, home, configPath string, jobs <-chan readerRequest, updates chan dataUpdate) {
	var cancel context.CancelFunc
	var done chan struct{}
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
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
			go func(job readerRequest) {
				defer close(finished)
				runReader(child, home, job.host, job.id, job.path, filepath.Dir(configPath), job.started, func(u dataUpdate) {
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
				})
			}(job)
		}
	}
}

func engine(ctx context.Context, cfg config.Config, configPath string, views <-chan viewSelection, publish func(dataUpdate)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	routes := make(chan route, 1)
	ipcDone := make(chan struct{})
	go func() { defer close(ipcDone); followIPC(ctx, views, routes) }()
	updates := make(chan dataUpdate, 1)
	jobs := make(chan readerRequest, 1)
	managerDone := make(chan struct{})
	go func() { defer close(managerDone); readerManager(ctx, dataHome(cfg), configPath, jobs, updates) }()
	defer func() { cancel(); <-managerDone; <-ipcDone }()
	var active string
	var activePath string
	var activeHost string
	var generation uint64
	var latest, previous dataUpdate
	var published bool
	var selectedAt time.Time
	var loadStarted time.Time
	var delivered int64
	var last route
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
		if last.ID == active && last.Live.ChatID == active {
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
		latest = dataUpdate{ID: active, Host: activeHost}
		if active != "" && activePath != "" {
			latest.Loading = true
			latest.Message = "Loading current chat…"
		} else if active != "" {
			latest.Message = "Waiting for Desktop current history branch"
		}
		job := readerRequest{id: active, host: activeHost, path: activePath, generation: generation, started: selectedAt}
		if activePath == "" {
			job.id = ""
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
	for {
		select {
		case <-ctx.Done():
			return
		case rt := <-routes:
			last = rt
			path := ""
			if rt.ID != "" && rt.Live.ChatID == rt.ID && rt.Live.Available && !rt.Live.Unsupported {
				path = rt.Live.RolloutPath
			}
			if active != rt.ID || activePath != path || activeHost != rt.Host {
				active, activePath, activeHost = rt.ID, path, rt.Host
				restartReader()
			}
			if active == "" {
				latest.Message = ipc.ViewMessage(rt.ViewReason)
				if !rt.Connected {
					latest.Message = "Waiting for Desktop connection"
				}
				emit()
			} else {
				schedule()
			}
		case u := <-updates:
			if u.ID == active && u.Generation == generation {
				immediate := (!latest.Snapshot.HasCounts || !latest.Timing.Started.Equal(u.Timing.Started)) && u.Snapshot.HasCounts || latest.Loading && !u.Loading
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

// Package lifecycle coordinates host replacement and bounded worker retries.
// It contains no window, data-reader or polling goroutines.
package lifecycle

import "time"

const (
	FallbackInterval = 30 * time.Second
	EventDelay       = 150 * time.Millisecond
	WorkerStopLimit  = 5 * time.Second
)

// Follow is owned by the watcher's GUI thread. Generations distinguish even
// reused Windows PIDs and late notifications from a previous Desktop process.
type Follow struct {
	PID              uint32
	Generation       uint64
	WorkerGeneration uint64
	RetryAt          time.Time
	failures         uint
	workerStarted    time.Time
}

func (f *Follow) Attach(pid uint32) bool {
	if pid == 0 || f.PID != 0 {
		return false
	}
	f.PID = pid
	f.Generation++
	f.failures, f.RetryAt = 0, time.Time{}
	return true
}

func (f *Follow) HostExited(generation uint64) bool {
	if f.PID == 0 || generation != f.Generation {
		return false
	}
	f.PID, f.failures, f.RetryAt = 0, 0, time.Time{}
	return true
}

func (f *Follow) Ready(now time.Time) bool {
	return f.PID != 0 && f.WorkerGeneration == 0 && !now.Before(f.RetryAt)
}

func (f *Follow) WorkerStarted(now time.Time) uint64 {
	if !f.Ready(now) {
		return 0
	}
	f.WorkerGeneration, f.workerStarted = f.Generation, now
	return f.WorkerGeneration
}

func (f *Follow) WorkerExited(generation uint64, now time.Time) bool {
	if generation == 0 || generation != f.WorkerGeneration {
		return false
	}
	f.WorkerGeneration = 0
	if f.PID != 0 && generation == f.Generation {
		if now.Sub(f.workerStarted) >= time.Minute {
			f.failures = 0
		}
		f.LaunchFailed(now)
	}
	return true
}

func (f *Follow) LaunchFailed(now time.Time) {
	if f.PID == 0 || f.WorkerGeneration != 0 {
		return
	}
	f.failures = min(f.failures+1, 5)
	f.RetryAt = now.Add(time.Duration(min(30, 1<<f.failures)) * time.Second)
}

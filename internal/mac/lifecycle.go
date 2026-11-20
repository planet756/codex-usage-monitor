package mac

import (
	"time"

	"codex-usage-monitor/internal/lifecycle"
)

type shutdownAction uint8

const (
	shutdownNone shutdownAction = iota
	shutdownTerminate
	shutdownKill
)

// The deadline belongs to the retiring worker, even after a new Desktop
// process has been attached. Only reaping that worker releases the launch gate.
type workerShutdown struct {
	generation uint64
	started    time.Time
}

func (s *workerShutdown) poll(f *lifecycle.Follow, now time.Time) shutdownAction {
	if f.WorkerGeneration == 0 || s.generation != f.WorkerGeneration {
		*s = workerShutdown{}
	}
	if f.WorkerGeneration == 0 {
		return shutdownNone
	}
	if s.generation == 0 {
		if f.PID != 0 && f.WorkerGeneration == f.Generation {
			return shutdownNone
		}
		s.generation, s.started = f.WorkerGeneration, now
		return shutdownTerminate
	}
	if now.Sub(s.started) >= lifecycle.WorkerStopLimit {
		return shutdownKill
	}
	return shutdownNone
}

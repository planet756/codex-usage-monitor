package mac

import (
	"testing"
	"time"

	"codex-usage-monitor/internal/lifecycle"
)

func TestWorkerStopDeadlineSurvivesRapidHostReplacement(t *testing.T) {
	now := time.Now()
	var follow lifecycle.Follow
	var shutdown workerShutdown
	follow.Attach(100)
	old := follow.WorkerStarted(now)
	if action := shutdown.poll(&follow, now); action != shutdownNone {
		t.Fatal("active Desktop worker was stopped")
	}
	follow.HostExited(follow.Generation)
	if action := shutdown.poll(&follow, now); action != shutdownTerminate {
		t.Fatal("retiring worker did not receive graceful shutdown")
	}
	follow.Attach(200)
	if action := shutdown.poll(&follow, now.Add(time.Second)); action != shutdownNone || follow.Ready(now) {
		t.Fatal("replacement overlapped the retiring worker")
	}
	if action := shutdown.poll(&follow, now.Add(lifecycle.WorkerStopLimit)); action != shutdownKill {
		t.Fatal("new Desktop PID disabled the old worker's kill deadline")
	}
	if follow.Ready(now.Add(time.Minute)) {
		t.Fatal("kill request released the launch gate before reaping")
	}
	follow.WorkerExited(old, now.Add(lifecycle.WorkerStopLimit))
	if !follow.Ready(now.Add(lifecycle.WorkerStopLimit)) {
		t.Fatal("reaped old worker prevented replacement launch")
	}
	// A very fast replacement launch can occur before the next shutdown poll.
	follow.WorkerStarted(now.Add(lifecycle.WorkerStopLimit))
	if action := shutdown.poll(&follow, now.Add(time.Minute)); action != shutdownNone || shutdown.generation != 0 {
		t.Fatal("stale deadline stopped the new generation")
	}
}

func TestWorkerStopAfterGracefulExitReturnsToSilentState(t *testing.T) {
	now := time.Now()
	var follow lifecycle.Follow
	var shutdown workerShutdown
	follow.Attach(100)
	old := follow.WorkerStarted(now)
	follow.HostExited(follow.Generation)
	shutdown.poll(&follow, now)
	follow.WorkerExited(old, now.Add(time.Second))
	if action := shutdown.poll(&follow, now.Add(time.Second)); action != shutdownNone || shutdown.generation != 0 || follow.Ready(now) {
		t.Fatal("worker exit left shutdown polling or a pending launch")
	}
}

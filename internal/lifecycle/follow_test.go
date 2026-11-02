package lifecycle

import (
	"testing"
	"time"
)

func TestRapidRestartWaitsForOldWorkerWithoutBackoff(t *testing.T) {
	now := time.Unix(100, 0)
	var f Follow
	if !f.Attach(42) || !f.Ready(now) {
		t.Fatal("first Desktop was not ready")
	}
	old := f.WorkerStarted(now)
	if !f.HostExited(old) || !f.Attach(42) { // Windows may reuse the same PID.
		t.Fatal("replacement Desktop was not attached")
	}
	if f.HostExited(old) || f.Ready(now) {
		t.Fatal("late host exit cleared the replacement or allowed overlapping workers")
	}
	if !f.WorkerExited(old, now.Add(time.Second)) || !f.Ready(now.Add(time.Second)) {
		t.Fatal("old worker exit delayed the new Desktop")
	}
	newGeneration := f.WorkerStarted(now.Add(time.Second))
	if newGeneration == old || f.WorkerExited(old, now.Add(2*time.Second)) || f.Ready(now.Add(2*time.Second)) {
		t.Fatal("late worker notification affected the new worker")
	}
}

func TestIdleDoesNotRelaunchAndCanFollowMuchLater(t *testing.T) {
	now := time.Unix(100, 0)
	var f Follow
	f.Attach(10)
	g := f.WorkerStarted(now)
	f.HostExited(g)
	f.WorkerExited(g, now.Add(time.Second))
	f.LaunchFailed(now)
	if f.Ready(now.Add(24*time.Hour)) || !f.RetryAt.IsZero() || f.WorkerGeneration != 0 {
		t.Fatal("idle listener tried to retain or restart a statistics worker")
	}
	if !f.Attach(11) || !f.Ready(now.Add(24*time.Hour)) {
		t.Fatal("listener could not follow a much later Desktop launch")
	}
}

func TestHostAndWorkerExitCanArriveInEitherOrder(t *testing.T) {
	for _, hostFirst := range []bool{true, false} {
		now := time.Unix(100, 0)
		var f Follow
		f.Attach(10)
		g := f.WorkerStarted(now)
		if hostFirst {
			f.HostExited(g)
			f.WorkerExited(g, now)
		} else {
			f.WorkerExited(g, now)
			f.HostExited(g)
		}
		if f.PID != 0 || f.WorkerGeneration != 0 || !f.RetryAt.IsZero() || f.Ready(now) {
			t.Fatalf("shutdown retained work, host-first=%t", hostFirst)
		}
		if !f.Attach(11) || !f.Ready(now) {
			t.Fatalf("new host inherited an old retry, host-first=%t", hostFirst)
		}
	}
}

func TestReplacementCanExitBeforeOldWorkerFinishes(t *testing.T) {
	now := time.Unix(100, 0)
	var f Follow
	if f.Attach(0) || f.WorkerStarted(now) != 0 || f.HostExited(0) {
		t.Fatal("idle state accepted an invalid host or worker")
	}
	f.Attach(10)
	g := f.WorkerStarted(now)
	if f.Attach(11) || f.WorkerStarted(now) != 0 {
		t.Fatal("active host/worker was replaced before exit")
	}
	f.HostExited(g)
	f.Attach(11)
	f.HostExited(f.Generation)
	f.WorkerExited(g, now)
	if f.Ready(now) || !f.RetryAt.IsZero() || f.WorkerGeneration != 0 {
		t.Fatal("late worker exit resurrected an already-closed replacement")
	}
}

func TestFailedAndShortLivedWorkersBackOff(t *testing.T) {
	now := time.Unix(100, 0)
	var f Follow
	f.Attach(10)
	for i, want := range []time.Duration{2, 4, 8, 16, 30, 30, 30} {
		if i%2 == 0 {
			f.LaunchFailed(now)
		} else {
			g := f.WorkerStarted(now)
			if g == 0 || !f.WorkerExited(g, now) {
				t.Fatal("could not simulate a short-lived worker")
			}
		}
		if f.RetryAt.Sub(now) != want*time.Second || f.Ready(f.RetryAt.Add(-time.Nanosecond)) || !f.Ready(f.RetryAt) {
			t.Fatalf("retry %d ignored its bounded delay", i)
		}
		now = f.RetryAt
	}
	g := f.WorkerStarted(now)
	f.WorkerExited(g, now.Add(time.Minute))
	if f.RetryAt.Sub(now.Add(time.Minute)) != 2*time.Second {
		t.Fatal("healthy worker did not reset earlier failure backoff")
	}
}

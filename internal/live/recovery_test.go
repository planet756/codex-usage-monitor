package live

import (
	"testing"
	"time"
)

func TestRecoveryTimerRetriesWithoutIncomingFramesAndStopsWhenHealthy(t *testing.T) {
	r := &Recovery{minimum: 10 * time.Millisecond, maximum: 40 * time.Millisecond}
	defer r.Close()
	if r.C() != nil {
		t.Fatal("idle recovery timer was armed")
	}
	r.Update(true)
	select {
	case <-r.C():
	case <-time.After(time.Second):
		t.Fatal("first request not scheduled")
	}
	r.Attempted()
	r.Update(true)
	for range 100 {
		r.Update(true)
	} // A frame burst cannot extend the deadline.
	select {
	case <-r.C():
	case <-time.After(time.Second):
		t.Fatal("rate-limited retry was lost without another frame")
	}
	r.Attempted()
	r.Update(true)
	if r.delay != 20*time.Millisecond {
		t.Fatal("retry backoff did not increase")
	}
	r.Update(false)
	if r.C() != nil || r.delay != 0 {
		t.Fatal("healthy stream retained retry work")
	}
}

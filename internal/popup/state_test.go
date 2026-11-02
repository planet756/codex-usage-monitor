package popup

import (
	"testing"
	"time"
)

func TestPopupHidesImmediatelyAndRequiresStableClose(t *testing.T) {
	now := time.Unix(100, 0)
	var s State
	s.Observe(1, Observation{Available: true, At: now})
	s.Observe(1, Observation{Available: true, Visible: true, Source: "aria_dialog", At: now.Add(time.Second)})
	if !s.Hidden {
		t.Fatal("open dialog did not hide the overlay")
	}
	s.Observe(1, Observation{Available: true, At: now.Add(1100 * time.Millisecond)})
	if !s.Hidden {
		t.Fatal("single clear frame exposed the underlying button")
	}
	s.Observe(1, Observation{Available: true, Visible: true, At: now.Add(1200 * time.Millisecond)})
	s.Observe(1, Observation{Available: true, At: now.Add(1300 * time.Millisecond)})
	s.Observe(1, Observation{Available: true, At: now.Add(1350 * time.Millisecond)})
	if !s.Hidden {
		t.Fatal("rapid clear frames bypassed close debounce")
	}
	s.Observe(1, Observation{Available: true, At: now.Add(1550 * time.Millisecond)})
	if s.Hidden {
		t.Fatal("closed dialog left the overlay hidden")
	}
}

func TestPopupTimeoutIsUnknownAndHostChangeDoesNotInheritOcclusion(t *testing.T) {
	now := time.Unix(100, 0)
	var s State
	s.Observe(1, Observation{Source: "uia_unavailable", At: now})
	if s.Hidden {
		t.Fatal("unavailable UIA hid a normal chat indefinitely")
	}
	s.Observe(1, Observation{Available: true, Visible: true, At: now.Add(time.Second)})
	s.Observe(1, Observation{Available: true, At: now.Add(2 * time.Second)})
	s.Observe(1, Observation{Source: "uia_timeout", At: now.Add(3 * time.Second)})
	s.Observe(1, Observation{Available: true, At: now.Add(4 * time.Second)})
	if !s.Hidden {
		t.Fatal("unknown sample was treated as a closed dialog")
	}
	s.Observe(1, Observation{Available: true, At: now.Add(5 * time.Second)})
	if s.Hidden {
		t.Fatal("healthy clear observations could not recover")
	}
	s.Observe(1, Observation{Available: true, Visible: true, At: now.Add(2 * time.Second)})
	if s.Hidden {
		t.Fatal("stale observation reversed the current state")
	}
	s.Observe(2, Observation{Source: "uia_unavailable", At: now.Add(6 * time.Second)})
	if s.Hidden || s.Window != 2 {
		t.Fatal("another window inherited popup state")
	}
}

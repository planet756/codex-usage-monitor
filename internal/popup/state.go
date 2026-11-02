// Package popup separates display occlusion from conversation subscriptions.
package popup

import "time"

type Observation struct {
	Available, Visible bool
	Source             string
	At                 time.Time
}

// State hides on the first positive observation. Reappearance needs two clear
// observations spanning 150 ms; a timeout cannot expose a known-open dialog.
type State struct {
	Window            uint64
	Hidden, Available bool
	Source            string
	Checked           time.Time
	clearSince        time.Time
	clearCount        int
}

func (s *State) Observe(window uint64, o Observation) {
	if s.Window != window {
		*s = State{Window: window}
	}
	if window == 0 || !s.Checked.IsZero() && o.At.Before(s.Checked) {
		return
	}
	s.Checked, s.Available, s.Source = o.At, o.Available, o.Source
	if !o.Available {
		s.clearSince, s.clearCount = time.Time{}, 0
		return
	}
	if o.Visible {
		s.Hidden = true
		s.clearSince, s.clearCount = time.Time{}, 0
		return
	}
	if !s.Hidden {
		return
	}
	if s.clearCount == 0 {
		s.clearSince = o.At
	}
	s.clearCount++
	if s.clearCount >= 2 && o.At.Sub(s.clearSince) >= 150*time.Millisecond {
		s.Hidden = false
		s.clearSince, s.clearCount = time.Time{}, 0
	}
}

// Package live tracks Desktop's revisioned source identity. Accounting belongs
// exclusively to the selected rollout reader.
package live

import (
	"codex-usage-monitor/internal/ipc"
	"fmt"
)

type View struct {
	ChatID                                string
	Available, Unsupported, OwnerVerified bool
	Revision, Frames, Resyncs, Skipped    int64
	RolloutPath                           string
}

type Stream struct {
	ID, Host, Self                            string
	view                                      View
	owner, pending                            string
	serial                                    uint64
	known, recovering, discovering, oversized bool
}

func (s *Stream) Reset(id, host, self string) {
	*s = Stream{ID: id, Host: host, Self: self, serial: s.serial + 1, discovering: id != "", recovering: id != ""}
	s.view.ChatID = id
}
func (s *Stream) View() View    { return s.view }
func (s *Stream) Owner() string { return s.owner }
func (s *Stream) NeedsResync() bool {
	return s.ID != "" && (s.discovering || s.recovering && !s.oversized && !s.view.Unsupported)
}
func (s *Stream) Discover() any {
	if !s.discovering || s.Self == "" {
		return nil
	}
	s.serial++
	s.pending = fmt.Sprintf("cum-owner-%d", s.serial)
	return map[string]any{"type": "request", "requestId": s.pending, "sourceClientId": s.Self, "version": 1, "method": "thread-owner-discovery", "timeoutMs": 3000,
		"params": map[string]string{"hostId": s.Host, "conversationId": s.ID}}
}
func (s *Stream) invalidate() {
	s.known = false
	s.view.Available = false
	s.view.RolloutPath = ""
	s.recovering = s.ID != ""
}
func (s *Stream) Skip()          { s.invalidate(); s.view.Skipped++ }
func (s *Stream) SkipOversized() { s.Skip(); s.oversized = true }

// The boolean result requests a fresh snapshot from the discovered owner.
// Unsolicited snapshots can prompt discovery but cannot perform a handover.
func (s *Stream) Apply(f *ipc.Frame) (changed, snapshot bool) {
	if s.ID == "" || f == nil {
		return false, false
	}
	if f.Type == "response" && f.Method == "thread-owner-discovery" {
		if s.pending == "" || f.RequestID != s.pending {
			return false, false
		}
		s.pending = ""
		if f.ResultType != "success" || f.HandledBy == "" {
			return false, false
		}
		s.discovering = false
		if s.owner != f.HandledBy {
			s.invalidate()
			s.owner = f.HandledBy
			s.view.OwnerVerified = true
			s.view.Revision = 0
			s.oversized = false
			s.view.Unsupported = false
		}
		return true, s.recovering
	}
	if f.Type != "broadcast" || f.Source == s.Self {
		return false, false
	}
	if len(f.Targets) > 0 {
		mine := false
		for _, target := range f.Targets {
			mine = mine || target == s.Self
		}
		if !mine {
			return false, false
		}
	}
	if f.Method == "ipc-connection-reset" || f.Method == "client-status-changed" && f.Params.Status == "disconnected" && f.Params.Client == s.owner && s.owner != "" {
		s.invalidate()
		s.owner = ""
		s.view.OwnerVerified = false
		s.pending = ""
		s.discovering = true
		return true, false
	}
	if f.Method != "thread-stream-state-changed" || f.Params.ID != s.ID || f.Params.Host != s.Host {
		return false, false
	}
	if f.Source != s.owner || s.owner == "" {
		s.discovering = true
		return true, false
	}
	if f.Version != 11 {
		s.invalidate()
		s.view.Unsupported = true
		return true, false
	}
	c := f.Params.Change
	if c.Type == "patches" && c.Revision <= s.view.Revision || c.Type == "snapshot" && c.Revision < s.view.Revision {
		return false, false
	}
	previousRevision := s.view.Revision
	// Even an unapplied delta proves that an older snapshot cannot certify the
	// current source. Availability, rather than this revision, certifies a path.
	s.view.Revision = c.Revision
	if f.SourceOnly && c.Type == "patches" {
		// Its unretained patches may replace rolloutPath. A fresh owner snapshot
		// supplies the source without pretending that this delta was applied.
		s.invalidate()
		s.view.Skipped++
		s.view.Resyncs++
		return true, true
	}
	if c.Type == "patches" && s.known && c.Base != previousRevision {
		s.invalidate()
		s.view.Resyncs++
	}
	path := s.view.RolloutPath
	assigned := c.Type == "snapshot"
	if assigned {
		path = string(c.State.RolloutPath)
	} else {
		for _, p := range c.Patches {
			if len(p.Path) == 0 {
				assigned = true
				path = ""
				if p.Op != "remove" {
					path = string(p.Value.Object.RolloutPath)
				}
			} else if len(p.Path) == 1 && p.Path[0] == "rolloutPath" {
				assigned = true
				path = ""
				if p.Op != "remove" {
					path = string(p.Value.Path)
				}
			}
		}
	}
	if !s.known && !assigned {
		return true, false
	}
	s.known = true
	s.recovering = false
	s.oversized = false
	s.view.Available = true
	s.view.Unsupported = false
	s.view.RolloutPath = path
	s.view.Frames++
	if f.SourceOnly {
		s.view.Skipped++
	}
	return true, false
}

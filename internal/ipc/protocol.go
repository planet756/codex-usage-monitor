// Package ipc implements the observed Desktop broadcast framing. This is not a
// public API; unrecognized frames cannot change the selected conversation.
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MaxFrame = 8 << 20

const MaxWireFrame = 256 << 20

// Large history snapshots retain only bounded routing/source metadata without
// allocating their payload. Subsequent patches stay on the same connection.
var ErrFrameSkipped = errors.New("IPC history frame exceeds in-memory budget")

// Separately identify an over-budget payload, so recovery does not repeatedly
// ask the owner to serialize the same large history. Is still matches skipped.
var ErrFrameOversized = fmt.Errorf("IPC frame exceeds projection budget: %w", ErrFrameSkipped)

// FrameReader owns one reusable small buffer. Parsing occurs before queueing,
// so a slow consumer never retains multiple raw history payloads.
type FrameReader struct{ scratch []byte }

func (r *FrameReader) Read(source io.Reader) (*Frame, error) {
	var h [4]byte
	if _, err := io.ReadFull(source, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(h[:]))
	if n == 0 || n > MaxWireFrame {
		return nil, errors.New("invalid IPC frame length")
	}
	if n > MaxFrame {
		return projectLargeSource(source, n)
	}
	if cap(r.scratch) < n {
		r.scratch = make([]byte, n)
	}
	b := r.scratch[:n]
	if cap(r.scratch) > 256<<10 {
		defer func() { r.scratch = nil }()
	}
	if _, err := io.ReadFull(source, b); err != nil {
		return nil, err
	}
	f, err := Decode(b)
	if errors.Is(err, ErrFrameSkipped) {
		return nil, ErrFrameOversized
	}
	return f, err
}

func WriteFrame(w io.Writer, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > MaxFrame {
		return errors.New("IPC frame too large")
	}
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(b)))
	for _, p := range [][]byte{h[:], b} {
		for len(p) > 0 {
			n, e := w.Write(p)
			if e != nil {
				return e
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			p = p[n:]
		}
	}
	return nil
}

type Selection struct{ ID, Host, Source string }

type Router struct {
	windows map[string]string
	clients map[string]string
	self    string
}

func (r *Router) SetSelf(id string) { r.self = id }

func (r *Router) Reset() { r.windows, r.clients, r.self = nil, nil, "" }

func (r *Router) Observe(m *Frame) bool {
	if m == nil || m.Type != "broadcast" {
		return false
	}
	if m.Source == r.self && r.self != "" {
		return false
	}
	if r.windows == nil {
		r.windows = make(map[string]string)
	}
	if m.Method == "client-status-changed" && m.Params.Client != "" && m.Params.ClientType != "" {
		if r.clients == nil {
			r.clients = make(map[string]string)
		}
		if len(r.clients) < 256 || r.clients[m.Params.Client] != "" {
			r.clients[m.Params.Client] = m.Params.ClientType
		}
	}
	if m.Method == "client-status-changed" && m.Params.Status == "disconnected" && m.Params.Client != "" {
		delete(r.clients, m.Params.Client)
		changed := false
		for k := range r.windows {
			if strings.HasPrefix(k, m.Params.Client+"\x1f") {
				delete(r.windows, k)
				changed = true
			}
		}
		return changed
	}
	p := m.Params
	if kind := r.clients[m.Source]; kind != "" && kind != "desktop" {
		return false
	}
	if m.Method != "thread-stream-following-changed" || m.Source == "" || p.Host == "" || !ValidThreadID(p.ID) || p.Following == nil {
		return false
	}
	// A Desktop client can retain several conversations. A follow notification
	// is a membership change, not a navigation or window-selection event.
	k := m.Source + "\x1f" + p.Host + "\x1f" + p.ID
	if *p.Following {
		if r.windows[k] == p.ID {
			return false
		}
		if len(r.windows) >= 64 {
			return false
		}
		r.windows[k] = p.ID
		return true
	}
	if r.windows[k] == p.ID {
		delete(r.windows, k)
		return true
	}
	return false
}

// Selected refuses ambiguous memberships; following alone cannot identify the
// selected page. Session additionally requires a verified native view identity.
func (r *Router) Selected() (string, int) {
	s, n := r.Selection()
	return s.ID, n
}

func (r *Router) Selection() (Selection, int) {
	var s Selection
	for key, id := range r.windows {
		parts := strings.SplitN(key, "\x1f", 3)
		if s.ID != "" && (s.ID != id || s.Host != parts[1]) {
			return Selection{}, len(r.windows)
		}
		if s.ID == "" || parts[0] < s.Source {
			s = Selection{ID: id, Source: parts[0], Host: parts[1]}
		}
	}
	return s, len(r.windows)
}

func (r *Router) SelectView(id, host string) Selection {
	if !ValidThreadID(id) {
		return Selection{}
	}
	var selected Selection
	for key, candidate := range r.windows {
		parts := strings.SplitN(key, "\x1f", 3)
		if candidate != id || host != "" && parts[1] != host {
			continue
		}
		if selected.ID != "" && selected.Host != parts[1] {
			return Selection{}
		}
		if selected.ID == "" || parts[0] < selected.Source {
			selected = Selection{ID: id, Host: parts[1], Source: parts[0]}
		}
	}
	// A freshly created chat need not have appeared in a retained-membership
	// announcement yet. The native page plus an explicit catalog/route host is
	// already sufficient to request its read-only stream snapshot.
	if selected.ID == "" && host != "" {
		return Selection{ID: id, Host: host}
	}
	return selected
}

func ValidThreadID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

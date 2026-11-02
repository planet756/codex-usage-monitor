package metrics

// Tests supply an in-memory membership store to the production admission gate.
// Production supplies the exact on-disk source witness through the same API.
type fixtureState struct {
	*State
	keys map[string]bool
}

func newTestState() *fixtureState { return &fixtureState{State: New(), keys: make(map[string]bool)} }
func (s *fixtureState) Observe(line []byte) {
	if e := s.ObserveWith(line, func(r *Record) (bool, error) {
		return s.State.Admit(r, func(key string) (bool, error) { old := s.keys[key]; s.keys[key] = true; return old, nil })
	}); e != nil {
		s.MarkIncomplete()
	}
}

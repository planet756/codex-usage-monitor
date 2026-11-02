package live

import "time"

// Recovery keeps a deferred retry alive even when the pipe delivers no further
// frames. It owns no goroutine and disables its timer completely when healthy.
type Recovery struct {
	timer                   *time.Timer
	ready                   <-chan time.Time
	last                    time.Time
	delay, minimum, maximum time.Duration
}

func NewRecovery() *Recovery            { return &Recovery{minimum: 4 * time.Second, maximum: 15 * time.Second} }
func (r *Recovery) C() <-chan time.Time { return r.ready }
func (r *Recovery) Close() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.ready = nil
}
func (r *Recovery) Update(pending bool) {
	if !pending {
		r.Close()
		r.last, r.delay = time.Time{}, 0
		return
	}
	if r.ready != nil {
		return
	}
	delay := time.Duration(0)
	if !r.last.IsZero() {
		delay = max(0, time.Until(r.last.Add(r.delay)))
	}
	if r.timer == nil {
		r.timer = time.NewTimer(delay)
	} else {
		r.timer.Reset(delay)
	}
	r.ready = r.timer.C
}
func (r *Recovery) Attempted() {
	r.ready = nil
	r.last = time.Now()
	if r.delay == 0 {
		r.delay = r.minimum
	} else {
		r.delay = min(r.maximum, r.delay*2)
	}
}

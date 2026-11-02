package metrics

import (
	"errors"
	"strings"
)

var ErrAccountingConflict = errors.New("conflicting accounting records")

// Admission uses an exact membership store supplied by the history reader.
// The same decisions govern replay, incremental reads and checkpoint resume.
func (s *State) Admit(r *Record, seen func(string) (bool, error)) (bool, error) {
	keys := r.accountingKeys(s.ThreadID, s.Turns[r.Payload.TurnID] == nil)
	if keys[0] != "" {
		duplicate, err := seen(keys[0])
		if err != nil {
			return false, err
		}
		matched := true
		if keys[1] != "" {
			matched, err = seen(keys[1])
			if err != nil {
				return false, err
			}
		}
		if duplicate {
			if !matched {
				return false, ErrAccountingConflict
			}
			return false, nil
		}
	}
	if keys[2] != "" {
		previous, err := seen(keys[2])
		if err != nil {
			return false, err
		}
		r.FirstSettledTurn = !previous
		r.CountResponse = true
	}
	if keys[3] != "" {
		previous, err := seen(keys[3])
		if err != nil {
			return false, err
		}
		r.PreviousTurn = previous
	}
	return true, nil
}

func (r Record) AccountingKeys(thread string) [4]string { return r.accountingKeys(thread, true) }

func (r Record) accountingKeys(thread string, timing bool) [4]string {
	key := r.AccountingKey(thread)
	keys := [4]string{key}
	if key != "" {
		if value := r.AccountingFingerprint(); value != "" {
			keys[1] = "value\x00" + key + "\x00" + value
		}
		keys[2] = r.SettledTurnKey()
	}
	p := r.Payload
	if p.TurnID == "" || p.ThreadID != "" && thread != "" && !strings.EqualFold(p.ThreadID, thread) {
		return keys
	}
	if timing && (r.Type == "token_usage_record" || r.Type == "event_msg" && (p.Type == "task_started" || p.Type == "task_complete" || p.Type == "turn_aborted" || p.Type == "item_completed" && timedItem(p.Item.Type))) {
		keys[3] = "timing-turn\x00" + p.TurnID
	}
	return keys
}

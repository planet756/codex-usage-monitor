package metrics

import (
	"math"
	"testing"
)

func TestCrossDaySpeedRemainsWeightedByDecodeTimeWithoutIdleGap(t *testing.T) {
	s := newTestState()
	start(s, "yesterday")
	item(s, "yesterday", "AgentMessage", 1000, 501000)
	response(s, "yesterday", "slow", 10000, 0, 501000) // 20 tok/s over 500 s.
	record(s, "event_msg", map[string]any{"type": "task_complete", "turn_id": "yesterday"})
	start(s, "today")
	item(s, "today", "AgentMessage", 86401000, 86501000)
	response(s, "today", "fast", 10000, 0, 86501000) // 100 tok/s over 100 s.
	x := s.Snapshot()
	if x.DecodeMS != 600000 || x.DecodeTokens != 20000 || x.TPS == nil || math.Abs(*x.TPS-100.0/3) > 1e-9 {
		t.Fatalf("calendar reset, idle gap or mean of rates changed the policy: %+v", x)
	}
}

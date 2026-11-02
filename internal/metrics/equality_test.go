package metrics

import "testing"

func TestSnapshotEqualityComparesOptionalValues(t *testing.T) {
	s := newTestState()
	start(s, "turn")
	usage(s, "turn", "response", 100, 70, 10)
	a, b := s.Snapshot(), s.Snapshot()
	if !a.Equal(b) {
		t.Fatal("equal numeric snapshots with distinct pointers differ")
	}
	*b.CachePercent += 1
	if a.Equal(b) {
		t.Fatal("changed optional numeric value was lost")
	}
	b = s.Snapshot()
	b.CachePercent = nil
	if a.Equal(b) {
		t.Fatal("unknown and known cache rates compare equal")
	}
}

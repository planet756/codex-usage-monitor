package live

import (
	"codex-usage-monitor/internal/ipc"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const chatA = "01a159db-a637-7512-8d64-06de0e0e785b"
const chatB = "01a15da4-16e9-7951-8fce-50f7bef8bb83"

func wire(id string, change map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"type": "broadcast", "method": "thread-stream-state-changed", "sourceClientId": "owner", "version": 11, "targetClientIds": []string{"monitor"}, "params": map[string]any{"conversationId": id, "hostId": "local", "change": change}})
	return b
}
func parseFrame(b []byte) *ipc.Frame {
	f, e := ipc.Decode(b)
	if e != nil {
		panic(e)
	}
	return f
}
func branchSnapshot(rev int64, path any) []byte {
	return wire(chatA, map[string]any{"type": "snapshot", "revision": rev, "conversationState": map[string]any{"rolloutPath": path}})
}
func branchPatch(base, rev int64, op string, path []any, value any) []byte {
	return wire(chatA, map[string]any{"type": "patches", "baseRevision": base, "revision": rev, "patches": []any{map[string]any{"op": op, "path": path, "value": value}}})
}
func discover(t *testing.T, s *Stream, owner string) {
	t.Helper()
	q := s.Discover().(map[string]any)
	f := &ipc.Frame{Type: "response", Method: "thread-owner-discovery", ResultType: "success", RequestID: q["requestId"].(string), HandledBy: owner}
	if changed, _ := s.Apply(f); !changed {
		t.Fatal("valid discovery rejected")
	}
}
func ready(t *testing.T) *Stream {
	t.Helper()
	s := &Stream{}
	s.Reset(chatA, "local", "monitor")
	discover(t, s, "owner")
	return s
}
func TestSourceRevisionAndExplicitPathChanges(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	s.Apply(parseFrame(branchPatch(1, 2, "replace", []any{"rolloutPath"}, "new.jsonl")))
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	s.Apply(parseFrame(branchPatch(1, 2, "replace", []any{"rolloutPath"}, "old.jsonl")))
	if s.View().RolloutPath != "new.jsonl" {
		t.Fatal("stale frame restored revoked history")
	}
	s.Apply(parseFrame(branchPatch(2, 3, "remove", []any{"rolloutPath"}, nil)))
	if s.View().RolloutPath != "" {
		t.Fatal("remove retained branch")
	}
	s.Apply(parseFrame(branchPatch(3, 4, "replace", []any{}, map[string]any{"rolloutPath": "root.jsonl"})))
	if s.View().RolloutPath != "root.jsonl" {
		t.Fatal("root replacement lost path")
	}
	s.Apply(parseFrame(branchSnapshot(5, nil)))
	if s.View().RolloutPath != "" || s.NeedsResync() {
		t.Fatal("bound empty snapshot was not preserved")
	}
}
func TestUsageCannotRecoverLostBranchOrEnterSourceState(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(1, "selected.jsonl")))
	f := parseFrame(branchPatch(98, 99, "replace", []any{"latestTokenUsageInfo", "total"}, map[string]int64{"inputTokens": 1000, "outputTokens": 999}))
	s.Apply(f)
	if s.View().RolloutPath != "" || !s.NeedsResync() {
		t.Fatal("usage baseline stopped source recovery")
	}
	s.Apply(parseFrame(branchPatch(99, 100, "replace", []any{"latestTokenUsageInfo", "total", "outputTokens"}, 20)))
	if !s.NeedsResync() {
		t.Fatal("scalar patch recovered lost source")
	}
	s.Apply(parseFrame(branchPatch(100, 101, "replace", []any{"rolloutPath"}, "selected.jsonl")))
	if s.NeedsResync() || s.View().RolloutPath != "selected.jsonl" {
		t.Fatal("independent source assignment failed to recover")
	}
	if strings.Contains(fmt.Sprint(s.View()), "999") {
		t.Fatal("accounting retained by source projection")
	}
}
func TestOwnerHandoverRequiresCorrelatedDiscovery(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(20, "old.jsonl")))
	f := parseFrame(branchSnapshot(1, "new.jsonl"))
	f.Source = "new-owner"
	s.Apply(f)
	if s.View().RolloutPath != "old.jsonl" || !s.NeedsResync() {
		t.Fatal("unsolicited source changed branch")
	}
	discover(t, s, "new-owner")
	if s.View().RolloutPath != "" {
		t.Fatal("handover kept old branch")
	}
	s.Apply(f)
	old := parseFrame(branchSnapshot(99, "old.jsonl"))
	s.Apply(old)
	if s.View().RolloutPath != "new.jsonl" {
		t.Fatal("former owner restored revoked history")
	}
	discover(t, s, "new-owner")
	if s.View().RolloutPath != "new.jsonl" || s.NeedsResync() {
		t.Fatal("same-owner confirmation disrupted valid branch")
	}
}
func TestDiscoveryIgnoresLateResponseAfterNavigationAndRetry(t *testing.T) {
	s := &Stream{}
	s.Reset(chatA, "local", "monitor")
	old := s.Discover().(map[string]any)
	s.Reset(chatB, "local", "monitor")
	next := s.Discover().(map[string]any)
	f := &ipc.Frame{Type: "response", Method: "thread-owner-discovery", RequestID: old["requestId"].(string), ResultType: "success", HandledBy: "old-owner"}
	if changed, _ := s.Apply(f); changed || s.Owner() != "" {
		t.Fatal("old chat response survived navigation")
	}
	f.RequestID = next["requestId"].(string)
	f.ResultType = "error"
	s.Apply(f)
	if !s.NeedsResync() {
		t.Fatal("failed discovery stopped retry")
	}
	discover(t, s, "new-owner")
	if s.Owner() != "new-owner" {
		t.Fatal("discovery did not recover")
	}
}
func TestSourceIsolationDisconnectBudgetAndVersion(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(1, "selected.jsonl")))
	before := s.View()
	for _, mode := range []string{"other_chat", "host", "target", "self"} {
		f := parseFrame(branchSnapshot(2, "wrong.jsonl"))
		switch mode {
		case "other_chat":
			f.Params.ID = chatB
		case "host":
			f.Params.Host = "remote"
		case "target":
			f.Targets = []string{"someone-else"}
		case "self":
			f.Source = "monitor"
		}
		s.Apply(f)
		if s.View() != before {
			t.Fatal("unrelated frame changed source", mode)
		}
	}
	s.SkipOversized()
	if s.NeedsResync() {
		t.Fatal("oversized snapshot entered retry loop")
	}
	s.Apply(parseFrame(branchPatch(99, 100, "replace", []any{"rolloutPath"}, "selected.jsonl")))
	if s.View().RolloutPath == "" {
		t.Fatal("explicit assignment failed")
	}
	f := parseFrame(branchSnapshot(101, "selected.jsonl"))
	f.Version = 12
	s.Apply(f)
	if !s.View().Unsupported || s.View().RolloutPath != "" || s.NeedsResync() {
		t.Fatal("unsupported version retained source")
	}
	s.Apply(parseFrame(branchSnapshot(102, "selected.jsonl")))
	if s.View().Unsupported {
		t.Fatal("supported snapshot did not recover")
	}
	disconnected := &ipc.Frame{Type: "broadcast", Method: "client-status-changed"}
	disconnected.Params.Client = "owner"
	disconnected.Params.Status = "disconnected"
	s.Apply(disconnected)
	if s.Owner() != "" || s.View().RolloutPath != "" || !s.NeedsResync() {
		t.Fatal("disconnect retained owner")
	}
}
func TestSourceOnlySnapshotDoesNotRequestLargeHistoryAgain(t *testing.T) {
	s := ready(t)
	f := parseFrame(branchSnapshot(10, "selected.jsonl"))
	f.SourceOnly = true
	s.Apply(f)
	if s.NeedsResync() || s.View().Skipped != 1 {
		t.Fatal("source-only snapshot failed", s.View())
	}
}

func TestSourceOnlyDeltaRequestsSnapshotInsteadOfSuppressingRecovery(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	f := parseFrame(branchPatch(1, 2, "replace", []any{"rolloutPath"}, "unretained.jsonl"))
	f.SourceOnly = true
	f.Params.Change.Patches = nil
	_, snapshot := s.Apply(f)
	if !snapshot || !s.NeedsResync() || s.View().RolloutPath != "" {
		t.Fatal("unretained delta kept an uncertified branch or blocked recovery")
	}
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	if s.View().RolloutPath != "" {
		t.Fatal("old snapshot revived a branch after an unretained delta")
	}
	s.Apply(parseFrame(branchSnapshot(2, "new.jsonl")))
	if s.NeedsResync() || s.View().RolloutPath != "new.jsonl" {
		t.Fatal("snapshot did not recover after a large delta")
	}
}

func TestRevisionGapFencesOldSnapshotsUntilCurrentRevision(t *testing.T) {
	s := ready(t)
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	s.Apply(parseFrame(branchPatch(98, 99, "replace", []any{"latestTokenUsageInfo", "total"}, map[string]int64{"inputTokens": 100, "outputTokens": 10})))
	s.Apply(parseFrame(branchSnapshot(1, "old.jsonl")))
	if !s.NeedsResync() || s.View().RolloutPath != "" || s.View().Revision != 99 {
		t.Fatal("old snapshot bypassed the observed revision floor")
	}
	s.Apply(parseFrame(branchSnapshot(99, "current.jsonl")))
	if s.NeedsResync() || s.View().RolloutPath != "current.jsonl" {
		t.Fatal("current snapshot did not restore the source")
	}
}

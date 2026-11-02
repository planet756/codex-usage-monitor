package rollout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func turnHistory(n int) string {
	var b strings.Builder
	b.WriteString(header)
	for i := range n {
		fmt.Fprintf(&b, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"audit-%d\"}}\n", i)
		fmt.Fprintf(&b, "{\"type\":\"token_usage_record\",\"payload\":{\"turn_id\":\"audit-%d\",\"response_id\":\"response-%d\",\"thread_token_usage\":{\"input_tokens\":%d,\"output_tokens\":%d}}}\n", i, i, (i+1)*100, (i+1)*10)
		fmt.Fprintf(&b, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\",\"turn_id\":\"audit-%d\"}}\n", i)
	}
	return b.String()
}

func TestLongHistoryFilterHitsAreVerifiedExactlyAndIndexIsReleased(t *testing.T) {
	d := t.TempDir()
	p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
	os.WriteFile(p, []byte(turnHistory(50000)), 0600)
	r := New(p)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	x := r.State.Snapshot()
	if x.Turns != 50000 || x.Steps != 50000 || !x.CountsComplete || r.seenIndex == nil {
		t.Fatalf("long history lost new turns or never exercised the exact index: %+v", x)
	}
	if len(r.State.Turns) > 32 || len(r.seen) != 4096 || len(r.seenIndex.pending) > indexRecord*256 {
		t.Fatal("long history exceeded the fixed RAM budgets")
	}
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	file := r.seenIndex.file
	name := file.Name()
	r.Close()
	if _, err := file.Stat(); err == nil {
		t.Fatal("temporary index handle remained open")
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("temporary index path survived close: %v", err)
	}
	if r.pending != nil || r.buffer != nil || r.verifyBuffer != nil || r.proofs != nil {
		t.Fatal("closed reader retained its working buffers")
	}
	resumed := New(p)
	defer resumed.Close()
	if !resumed.RestoreCheckpoint(c) {
		t.Fatal("long history failed checkpoint restore")
	}
	appendFile(t, p, strings.Replace(usageLine, `"turn_id":"t"`, `"turn_id":"audit-0"`, 1))
	if _, err := resumed.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resumed.State.Snapshot().Steps != 50001 || resumed.State.Snapshot().Turns != 50000 {
		t.Fatal("late unique response lost or retired turn recounted after restore")
	}
	old := resumed.seenIndex.file
	os.WriteFile(p, []byte(header+usageLine), 0600)
	resumed.Update(context.Background())
	if resumed.seenIndex != nil {
		t.Fatal("rewritten source retained its historical index")
	}
	if _, err := old.Stat(); err == nil {
		t.Fatal("source reset leaked the old index handle")
	}
}

func TestExactIndexHandlesDiskAndBufferedChains(t *testing.T) {
	x, err := newTurnIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	for i := range 20000 {
		if err := x.add(fmt.Sprintf("id-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 20000 {
		if found, err := x.contains(fmt.Sprintf("id-%d", i)); err != nil || !found {
			t.Fatalf("lost indexed id %d: %v", i, err)
		}
	}
	for i := range 1000 {
		if found, err := x.contains(fmt.Sprintf("foreign-%d", i)); err != nil || found {
			t.Fatalf("fabricated membership: %v", err)
		}
	}
}

func TestIndexFailureRebuildsFromVerifiedSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	os.WriteFile(p, []byte(turnHistory(100)), 0600)
	r := New(p)
	defer r.Close()
	r.seen = make([]uint64, 4096)
	for i := range r.seen {
		r.seen[i] = ^uint64(0)
	}
	r.Update(context.Background())
	if r.State.TurnCount != 100 || !r.State.Snapshot().CountsComplete {
		t.Fatal("filter positives lost new turns")
	}
	// Make the flushed branch unavailable and confirm source-based recovery.
	for i := range 500 {
		r.seenIndex.add(fmt.Sprintf("extra-%d", i))
	}
	r.seenIndex.file.Close()
	appendFile(t, p, strings.Replace(usageLine, `"turn_id":"t"`, `"turn_id":"audit-0"`, 1))
	if _, err := r.Update(context.Background()); err != nil || r.State.StepCount != 101 || r.State.TurnCount != 100 {
		t.Fatalf("index read failure fabricated an old turn: %v", err)
	}
}

func TestExactWitnessIgnoresForeignAndUnrelatedRecords(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	foreign := `{"type":"token_usage_record","payload":{"thread_id":"other-session","turn_id":"t","response_id":"foreign"}}` + "\n"
	unrelated := `{"type":"response_item","payload":{"turn_id":"t"}}` + "\n"
	start := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}` + "\n"
	os.WriteFile(p, []byte(header+foreign+unrelated+start+usageLine), 0600)
	r := New(p)
	defer r.Close()
	r.seen = make([]uint64, 4096)
	for i := range r.seen {
		r.seen[i] = ^uint64(0)
	}
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.State.TurnCount != 1 || r.State.StepCount != 1 || !r.State.Snapshot().CountsComplete {
		t.Fatal("unrelated metadata fabricated retired membership")
	}
}

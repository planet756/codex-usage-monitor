package rollout

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const header = "{\"type\":\"session_meta\",\"payload\":{\"id\":\"test-session\"}}\n"
const usageLine = "{\"type\":\"token_usage_record\",\"payload\":{\"turn_id\":\"t\",\"response_id\":\"r\",\"thread_token_usage\":{\"input_tokens\":100,\"cached_input_tokens\":80,\"output_tokens\":20}}}\n"

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString(s)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
}

func TestPartialAppendTruncationAndReplacement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	_ = os.WriteFile(p, []byte(header+usageLine[:40]), 0600)
	r := New(p)
	if _, e := r.Update(context.Background()); e != nil {
		t.Fatal(e)
	}
	if r.State.HasUsage {
		t.Fatal("parsed unfinished record")
	}
	appendFile(t, p, usageLine[40:])
	if changed, e := r.Update(context.Background()); e != nil || !changed {
		t.Fatalf("append: %v", e)
	}
	if r.State.Snapshot().Total != 120 {
		t.Fatal("wrong incremental total")
	}
	if changed, e := r.Update(context.Background()); e != nil || changed {
		t.Fatal("reparsed an unchanged file")
	}
	_ = os.WriteFile(p, []byte(header), 0600)
	_, _ = r.Update(context.Background())
	if r.State.HasUsage {
		t.Fatal("retained counters after truncate")
	}
	replacement := filepath.Join(filepath.Dir(p), "replacement")
	_ = os.WriteFile(replacement, []byte(strings.ReplaceAll(header, "test-session", "new-session")+usageLine), 0600)
	if e := os.Rename(replacement, p); e != nil {
		t.Fatal(e)
	}
	_, _ = r.Update(context.Background())
	if r.State.ThreadID != "new-session" || r.State.Snapshot().Total != 120 {
		t.Fatal("replacement was not detected")
	}
}

func TestCheckpointDoesNotPersistPartialMessageAndRestoresExactly(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "rollout")
	cache := filepath.Join(d, "cache.json")
	partial := `{"type":"response_item","payload":{"text":"PRIVATE_MESSAGE_SENTINEL`
	_ = os.WriteFile(p, []byte(header+usageLine+partial), 0600)
	r := New(p)
	_, _ = r.Update(context.Background())
	if e := r.SaveCheckpoint(cache); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(cache)
	if bytes.Contains(b, []byte("PRIVATE_MESSAGE_SENTINEL")) {
		t.Fatal("message content entered aggregate cache")
	}
	appendFile(t, p, "\"}}\n")
	r2 := New(p)
	if !r2.RestoreCheckpoint(cache) {
		t.Fatal("valid checkpoint rejected")
	}
	_, e := r2.Update(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	full := New(p)
	_, _ = full.Update(context.Background())
	if r2.State.Lines != full.State.Lines || r2.State.Snapshot().Total != full.State.Snapshot().Total {
		t.Fatal("checkpoint replay duplicated or lost records")
	}
	_ = os.WriteFile(cache, []byte(`{"Version":1`), 0600)
	if New(p).RestoreCheckpoint(cache) {
		t.Fatal("accepted a corrupt cache")
	}
}

func TestOversizedRecordMemoryIsBoundedAndFollowingUsageRecovers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout")
	f, _ := os.Create(p)
	_, _ = f.WriteString(header + `{"type":"response_item","payload":"`)
	block := bytes.Repeat([]byte{'x'}, 1<<20)
	for range 9 {
		_, _ = f.Write(block)
	}
	_, _ = f.WriteString("\"}\n" + usageLine)
	f.Close()
	r := New(p)
	_, e := r.Update(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if r.State.Oversized != 1 || r.State.HistoryComplete || r.State.Snapshot().Total != 120 || len(r.pending) != 0 {
		t.Fatalf("oversize recovery failed: %+v", r.State.Snapshot())
	}
}

func TestDecodeCheckpointRestoresAnOpenResponseWithoutDoubleCounting(t *testing.T) {
	d := t.TempDir()
	p, cache := filepath.Join(d, "rollout"), filepath.Join(d, "cache.json")
	first := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"a"}}
{"type":"event_msg","payload":{"type":"item_completed","turn_id":"a","item":{"type":"AgentMessage"},"started_at_ms":1000,"completed_at_ms":2000}}
{"type":"token_usage_record","timestamp":"1970-01-01T00:00:02Z","payload":{"turn_id":"a","response_id":"r1","usage":{"input_tokens":100,"output_tokens":100}}}
{"type":"event_msg","payload":{"type":"item_completed","turn_id":"a","item":{"type":"Reasoning"},"started_at_ms":3000,"completed_at_ms":4000}}
`
	_ = os.WriteFile(p, []byte(header+first), 0600)
	r := New(p)
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	restored := New(p)
	if !restored.RestoreCheckpoint(cache) {
		t.Fatal("decode checkpoint rejected")
	}
	closing := `{"type":"token_usage_record","timestamp":"1970-01-01T00:00:05Z","payload":{"turn_id":"a","response_id":"r2","usage":{"input_tokens":100,"output_tokens":300,"reasoning_output_tokens":200}}}
`
	appendFile(t, p, closing+closing)
	if _, err := restored.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	full := New(p)
	if _, err := full.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, b := restored.State.Snapshot(), full.State.Snapshot()
	if a.Turns != 1 || a.Steps != 2 || a.Turns != b.Turns || a.Steps != b.Steps || a.DecodeSamples != 2 || a.DecodeTokens != 400 || a.DecodeMS != 3000 || a.TPS == nil ||
		b.TPS == nil || *a.TPS != *b.TPS || a.DecodeMS != b.DecodeMS {
		t.Fatalf("restart duplicated or lost response accounting: restored=%+v full=%+v", a, b)
	}
	bs, _ := os.ReadFile(cache)
	versionField := fmt.Sprintf(`"Version":%d`, checkpointVersion)
	bs = []byte(strings.Replace(string(bs), versionField, `"Version":99`, 1))
	_ = os.WriteFile(cache, bs, 0600)
	if New(p).RestoreCheckpoint(cache) {
		t.Fatal("accepted a cache without whole-chat turn and step counts")
	}
	bs, _ = os.ReadFile(cache)
	bs = []byte(strings.Replace(string(bs), `"Version":99`, versionField, 1))
	bs = []byte(strings.Replace(string(bs), `"Format":"cum-history-prefixes",`, "", 1))
	_ = os.WriteFile(cache, bs, 0600)
	if New(p).RestoreCheckpoint(cache) {
		t.Fatal("accepted a cache without the current format identity")
	}
}

package rollout

import (
	"bytes"
	"codex-usage-monitor/internal/metrics"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExactIndexResumeAndSafeFallback(t *testing.T) {
	for _, mode := range []string{"intact", "missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			a := fixtureTurn("first-private-turn", 1000, 1000, 100)
			b := fixtureTurn("second-private-turn", 10000, 2000, 200)
			records := append(append(append([]testRecord{}, a...), b...), a...)
			records = append(records, fixtureTurn("third-private-turn", 20000, 3000, 300)...)
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
			h := resolveFixture(t, home, locateChat)
			want := readFixture(t, h).State.Snapshot()
			cut := f.offsets[1+len(a)+len(b)]
			if err := os.WriteFile(f.path, f.data[:cut], 0600); err != nil {
				t.Fatal(err)
			}
			r := readFixture(t, h)
			// Force the source-witness path before saving even on a tiny fixture.
			source, err := os.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			r.lookupFile, r.lookupContext, r.lookupEnd = source, context.Background(), r.Offset
			if _, err := r.rebuildSeen("not-a-key"); err != nil {
				t.Fatal(err)
			}
			source.Close()
			r.lookupFile, r.lookupContext = nil, nil
			cache := filepath.Join(t.TempDir(), "checkpoint.json")
			if err := r.SaveCheckpoint(cache); err != nil {
				t.Fatal(err)
			}
			sidecar := indexCachePath(cache, "seen")
			data, err := os.ReadFile(sidecar)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("private-turn")) {
				t.Fatal("plaintext identity persisted")
			}
			if mode == "missing" {
				if err := os.Remove(sidecar); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "corrupt" {
				data[len(data)-1] ^= 1
				if err := os.WriteFile(sidecar, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			next := NewHistory(h)
			defer next.Close()
			if !next.RestoreCheckpoint(cache) {
				t.Fatal("aggregate restore failed")
			}
			if (next.seenIndex != nil) != (mode == "intact") {
				t.Fatal("index integrity gate failed")
			}
			appendFile, err := os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = appendFile.Write(f.data[cut:])
			appendFile.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := next.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := next.State.Snapshot(); !got.Equal(want) || got.Steps != 3 || got.Turns != 3 {
				t.Fatalf("resume/fallback changed accounting: %+v", got)
			}
			index := next.seenIndex
			next.Close()
			if index != nil {
				requireClosedFile(t, index.file)
			}
		})
	}
}

func TestIndexCreatedByFingerprintRetainsCurrentResponseKey(t *testing.T) {
	home := t.TempDir()
	batch := fixtureTurn("a", 1000, 1000, 100)
	records := append(append([]testRecord{}, batch...), batch[3])
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	h := resolveFixture(t, home, locateChat)
	cut := f.offsets[4]
	if err := os.WriteFile(f.path, f.data[:cut], 0600); err != nil {
		t.Fatal(err)
	}
	r := readFixture(t, h)
	var response metrics.Record
	if err := json.Unmarshal(f.data[f.offsets[4]:f.offsets[5]], &response); err != nil {
		t.Fatal(err)
	}
	key := response.AccountingKey(locateChat)
	fingerprint := "value\x00" + key + "\x00" + response.AccountingFingerprint()
	for _, bit := range seenBits(fingerprint) {
		r.seen[bit/64] |= uint64(1) << (bit % 64)
	}
	r.closeSeenIndex()
	appendFile, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = appendFile.Write(f.data[cut:])
	appendFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if x := r.State.Snapshot(); x.Steps != 1 || x.Turns != 1 || r.seenIndex == nil {
		t.Fatalf("same-record index transition double counted: %+v", x)
	}
}

func TestConflictAfterCheckpointIsStickyUntilSourceChanges(t *testing.T) {
	home := t.TempDir()
	a := fixtureTurn("a", 1000, 1000, 100)
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, a...)
	h := resolveFixture(t, home, locateChat)
	r := readFixture(t, h)
	cache := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	next := NewHistory(h)
	defer next.Close()
	if !next.RestoreCheckpoint(cache) {
		t.Fatal("restore")
	}
	conflict := testRecord{"token_usage_record", map[string]any{"turn_id": "a", "response_id": "response-a", "usage": map[string]any{"input_tokens": 100, "output_tokens": 900}}, 2500}
	updated := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(a, conflict)...)
	if _, err := next.Update(context.Background()); !errors.Is(err, ErrAccountingConflict) {
		t.Fatalf("conflict accepted: %v", err)
	}
	state := next.State
	for range 3 {
		if _, err := next.Update(context.Background()); !errors.Is(err, ErrAccountingConflict) || next.State != state {
			t.Fatal("unchanged conflict needlessly replayed")
		}
	}
	if next.State.Snapshot().HistoryComplete {
		t.Fatal("conflict certified complete")
	}
	if err := os.WriteFile(updated.path, f.data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := next.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := next.State.Snapshot(); got.Steps != 1 || !got.HistoryComplete {
		t.Fatalf("corrected source did not recover: %+v", got)
	}
}

func TestUntrustedStampVerifiesDigestAndRejectsChangedSource(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	cache := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var cp checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	for i := range cp.Parts {
		cp.Parts[i].Stamp.Trusted = false
	}
	cp.Integrity, _ = cp.integrity()
	raw, _ = json.Marshal(cp)
	if err := os.WriteFile(cache, raw, 0600); err != nil {
		t.Fatal(err)
	}
	next := NewHistory(*r.history)
	defer next.Close()
	if !next.RestoreCheckpoint(cache) || next.verifyBuffer == nil {
		t.Fatal("unsupported stamps bypassed digest verification")
	}
	modified := bytes.Replace(f.data, []byte(`"input_tokens":1000`), []byte(`"input_tokens":9000`), 1)
	if err := os.WriteFile(f.path, modified, 0600); err != nil {
		t.Fatal(err)
	}
	bad := NewHistory(*r.history)
	defer bad.Close()
	if bad.RestoreCheckpoint(cache) {
		t.Fatal("changed source accepted")
	}
}

func TestCanceledIndexRestoreLeavesReaderUntouched(t *testing.T) {
	home := t.TempDir()
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	cache := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	next := NewHistory(*r.history)
	defer next.Close()
	before := next.State
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if next.RestoreCheckpointContext(ctx, cache) || next.State != before || next.Offset != 0 {
		t.Fatal("canceled restore mutated state")
	}
}

type proofMutationContext struct {
	context.Context
	reader *Reader
	fired  bool
	mutate func()
}

func (c *proofMutationContext) Err() error {
	if !c.fired && c.reader.verifyBuffer != nil {
		c.fired = true
		c.mutate()
	}
	return c.Context.Err()
}

func TestAppendDuringProofRetriesWithoutCertifyingRewrite(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "append", true: "append_and_rewrite"}[rewrite], func(t *testing.T) {
			home := t.TempDir()
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			r := readFixture(t, resolveFixture(t, home, locateChat))
			before := r.State.Snapshot()
			r.proofs[0].Stamp.Trusted = false
			ctx := &proofMutationContext{Context: context.Background(), reader: r, mutate: func() {
				data := append(append([]byte{}, f.data...), '\n')
				if rewrite {
					data = bytes.Replace(data, []byte(`"output_tokens":100`), []byte(`"output_tokens":900`), 1)
				}
				if err := os.WriteFile(f.path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}}
			err := r.SaveCheckpointContext(ctx, filepath.Join(t.TempDir(), "checkpoint.json"))
			if !ctx.fired {
				t.Fatal("mutation not exercised")
			}
			if rewrite {
				if !errors.Is(err, ErrHistoryChanged) {
					t.Fatalf("rewrite accepted: %v", err)
				}
			} else if !errors.Is(err, ErrSourceBusy) || !r.State.Snapshot().Equal(before) {
				t.Fatalf("safe append lost parsed state: %v", err)
			}
		})
	}
}

func TestEarlierSegmentRewriteDuringLaterProofIsRejected(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	base := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 10000, 2000, 200)...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	r.proofs[1].Stamp.Trusted = false
	ctx := &proofMutationContext{Context: context.Background(), reader: r, mutate: func() {
		data := bytes.Replace(root.data, []byte(`"output_tokens":100`), []byte(`"output_tokens":900`), 1)
		if err := os.WriteFile(root.path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	err := r.SaveCheckpointContext(ctx, filepath.Join(t.TempDir(), "checkpoint.json"))
	if !ctx.fired || !errors.Is(err, ErrHistoryChanged) {
		t.Fatalf("earlier segment rewrite was missed: fired=%t err=%v", ctx.fired, err)
	}
}

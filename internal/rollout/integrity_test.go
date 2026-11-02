package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMiddleRewriteThenAppendCannotSurviveCheckpoint(t *testing.T) {
	d := t.TempDir()
	p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
	old := header + strings.Repeat(" ", 700) + "\n" + usageLine + strings.Repeat(" ", 700) + "\n"
	os.WriteFile(p, []byte(old), 0600)
	r := New(p)
	defer r.Close()
	r.Update(context.Background())
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(c)
	fresh := strings.Replace(old, `"input_tokens":100`, `"input_tokens":900`, 1) + `{"type":"response_item","payload":{}}` + "\n"
	os.WriteFile(p, []byte(fresh), 0600)
	if r.SaveCheckpoint(c) == nil {
		t.Fatal("saved stale state over a rewritten-and-extended prefix")
	}
	after, _ := os.ReadFile(c)
	if string(after) != string(good) {
		t.Fatal("failed integrity check replaced the valid cache")
	}
	resumed := New(p)
	defer resumed.Close()
	if resumed.RestoreCheckpoint(c) {
		t.Fatal("restored stale middle content after file growth")
	}
	for _, reader := range []*Reader{r, resumed} {
		if _, err := reader.Update(context.Background()); err != nil || reader.State.Snapshot().Total != 920 {
			t.Fatalf("rewrite+append replay failed: %v %+v", err, reader.State.Snapshot())
		}
	}
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	final := New(p)
	defer final.Close()
	if !final.RestoreCheckpoint(c) || final.State.Snapshot().Total != 920 {
		t.Fatal("new content did not round trip")
	}
}

func TestTimestampOnlyChangeKeepsAggregateAndAcceptsVerifiedCache(t *testing.T) {
	d := t.TempDir()
	p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
	os.WriteFile(p, []byte(header+strings.Repeat(usageLine, 1000)), 0600)
	r := New(p)
	defer r.Close()
	r.Update(context.Background())
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	state, lines, offset := r.State, r.State.Lines, r.Offset
	stamp := r.info.ModTime().Add(time.Second)
	os.Chtimes(p, stamp, stamp)
	if changed, err := r.Update(context.Background()); err != nil || changed || r.State != state || r.State.Lines != lines || r.Offset != offset {
		t.Fatalf("timestamp-only change reparsed history: changed=%t err=%v", changed, err)
	}
	resumed := New(p)
	defer resumed.Close()
	if !resumed.RestoreCheckpoint(c) || resumed.State.Lines != lines {
		t.Fatal("timestamp-only change invalidated an intact prefix")
	}
}

func TestPendingRecordCapacityAndCompletionRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	prefix := `{"type":"response_item","payload":"`
	data := prefix + strings.Repeat("x", MaxRecordBytes-len(prefix)-4)
	os.WriteFile(p, []byte(data), 0600)
	r := New(p)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cap(r.pending) > MaxRecordBytes || len(r.pending) != len(data) {
		t.Fatalf("pending buffer exceeded budget: len=%d cap=%d", len(r.pending), cap(r.pending))
	}
	appendFile(t, p, "\"}\n")
	if _, err := r.Update(context.Background()); err != nil || cap(r.pending) != 0 || r.State.Oversized != 0 {
		t.Fatalf("completed large record was not released: %v", err)
	}
}

func TestDroppingCheckpointResumesWithoutLosingFollowingUsage(t *testing.T) {
	d := t.TempDir()
	p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
	os.WriteFile(p, []byte(header+strings.Repeat("x", MaxRecordBytes+1000)), 0600)
	r := New(p)
	defer r.Close()
	r.Update(context.Background())
	if !r.dropping {
		t.Fatal("oversized unfinished record was not dropped")
	}
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	resumed := New(p)
	defer resumed.Close()
	if !resumed.RestoreCheckpoint(c) {
		t.Fatal("dropped-record prefix did not restore")
	}
	appendFile(t, p, "\n"+usageLine)
	if _, err := resumed.Update(context.Background()); err != nil || resumed.State.Snapshot().Total != 120 || resumed.State.Oversized != 1 || len(resumed.pending) != 0 {
		t.Fatalf("oversized resume failed: %v", err)
	}
}

type limitedContext struct {
	context.Context
	remaining int
	cancel    context.CancelFunc
}

func (c *limitedContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFullPrefixVerificationIsCancellableWithoutResettingState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	os.WriteFile(p, []byte(header+strings.Repeat(usageLine, 10000)), 0600)
	r := New(p)
	defer r.Close()
	r.Update(context.Background())
	state, offset := r.State, r.Offset
	appendFile(t, p, "\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limited := &limitedContext{Context: ctx, remaining: 3, cancel: cancel}
	if _, err := r.Update(limited); !errors.Is(err, context.Canceled) || r.State != state || r.Offset != offset {
		t.Fatal("prefix verification ignored cancellation or mutated settled state")
	}
}

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

func TestNewHeadResumesVerifiedParentCheckpoint(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compressed], func(t *testing.T) {
			home, cache := t.TempDir(), t.TempDir()
			batch := fixtureTurn("a", 1000, 1000, 100)
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, batch[:3]...)
			path := root.path
			if compressed {
				path = compressFixture(t, path, true)
			}
			r := readFixture(t, resolveFixture(t, home, locateChat))
			if err := r.SaveCheckpoint(checkpointPath(cache, path)); err != nil {
				t.Fatal(err)
			}
			base := root.ref(len(root.offsets) - 1)
			fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, batch[3:]...)
			h := resolveFixture(t, home, locateChat)
			next := NewHistory(h)
			defer next.Close()
			mode, ok := next.RestoreBest(context.Background(), cache)
			if !ok || mode != "prefix" || next.Offset != int64(len(root.data)) {
				t.Fatalf("parent prefix not restored: %s %t", mode, ok)
			}
			if _, err := next.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := readFixture(t, h).State.Snapshot()
			if !next.State.Snapshot().Equal(want) || want.Steps != 1 || want.DecodeSamples != 1 || want.DecodeMS != 1000 {
				t.Fatal("open response was lost or counted twice across cached boundary")
			}
		})
	}
}

func TestChunkedProgressAndPrefixCacheMatchFullReplay(t *testing.T) {
	home, cache := t.TempDir(), t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	ref := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref, fixtureTurn("b", 10000, 2000, 200)...)
	h := resolveFixture(t, home, locateChat)
	expected := readFixture(t, h).State.Snapshot()
	tracked := Tracked{ID: locateChat, CacheDir: cache, Incremental: true, Resolve: func() (History, error) { return h, nil }}
	defer func() {
		if tracked.Reader != nil {
			tracked.Reader.Close()
		}
	}()
	if _, _, err := tracked.Update(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !tracked.Reader.Loading() || tracked.Snapshot().CountsComplete || tracked.Snapshot().HasUsage || tracked.Snapshot().TPS != nil || tracked.Snapshot().Steps != 1 {
		t.Fatal("partial progress was hidden or certified complete")
	}
	if tracked.Reader.chunkSource == nil {
		t.Fatal("chunked load repeatedly reopens source handles")
	}
	if _, err := os.Stat(prefixPath(cache, h, 1)); err != nil {
		t.Fatal("verified boundary checkpoint missing")
	}
	for tracked.Reader.Loading() {
		if _, _, err := tracked.Update(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !tracked.Snapshot().Equal(expected) {
		t.Fatal("chunking changed final accounting")
	}
	if tracked.Timing.Mode != "rebuild" || tracked.Timing.FirstCountsMS <= 0 || tracked.Timing.ReadyMS < tracked.Timing.FirstCountsMS || tracked.Timing.ReadBytes != tracked.Reader.Offset {
		t.Fatalf("stage timings are inconsistent: %+v", tracked.Timing)
	}
	before := tracked.Timing
	if _, _, err := tracked.Update(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if tracked.Timing != before {
		t.Fatal("idle poll changed completed load timings")
	}
	next := NewHistory(h)
	defer next.Close()
	if mode, ok := next.RestoreBest(context.Background(), cache); !ok || mode != "prefix" {
		t.Fatal("stored boundary was not reused")
	}
	if _, err := next.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !next.State.Snapshot().Equal(expected) {
		t.Fatal("prefix cache changed final accounting")
	}
}

func TestPrefixCannotCrossRevertOrRewrite(t *testing.T) {
	for _, mode := range []string{"revert", "rewrite"} {
		t.Run(mode, func(t *testing.T) {
			home, cache := t.TempDir(), t.TempDir()
			first := fixtureTurn("a", 1000, 1000, 100)
			all := append(first, fixtureTurn("discarded", 10000, 1000, 900)...)
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, all...)
			r := readFixture(t, resolveFixture(t, home, locateChat))
			if err := r.SaveCheckpoint(checkpointPath(cache, root.path)); err != nil {
				t.Fatal(err)
			}
			cut := len(root.offsets) - 1
			if mode == "revert" {
				cut = 1 + len(first)
			} else {
				data := strings.Replace(string(root.data), `"output_tokens":100`, `"output_tokens":200`, 1)
				if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ref := root.ref(cut)
			fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref, fixtureTurn("child", 20000, 1000, 100)...)
			h := resolveFixture(t, home, locateChat)
			next := NewHistory(h)
			defer next.Close()
			if _, ok := next.RestoreBest(context.Background(), cache); ok || next.Offset != 0 {
				t.Fatal("checkpoint crossed a revert or changed prefix")
			}
			if _, err := next.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !next.State.Snapshot().Equal(readFixture(t, h).State.Snapshot()) {
				t.Fatal("fallback rebuild changed accounting")
			}
		})
	}
}

func TestChunkedCancellationClosesPinnedSources(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	ref := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref, fixtureTurn("b", 10000, 1000, 100)...)
	r := NewHistory(resolveFixture(t, home, locateChat))
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := r.UpdateChunk(ctx); err != nil {
		t.Fatal(err)
	}
	file := r.chunkSource.(*historyFile).files[0]
	cancel()
	if _, err := r.UpdateChunk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	requireClosedFile(t, file)
	if _, err := os.Stat(filepath.Dir(root.path)); err != nil {
		t.Fatal(err)
	}
}

func TestRepresentationTransitionReusesVerifiedDecodedPrefix(t *testing.T) {
	for _, materialize := range []bool{false, true} {
		t.Run(map[bool]string{false: "compress", true: "materialize"}[materialize], func(t *testing.T) {
			home, cache := t.TempDir(), t.TempDir()
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			path := f.path
			if materialize {
				path = compressFixture(t, path, true)
			}
			r := readFixture(t, resolveFixture(t, home, locateChat))
			want := r.State.Snapshot()
			if err := r.SaveCheckpoint(checkpointPath(cache, path)); err != nil {
				t.Fatal(err)
			}
			if materialize {
				if err := os.WriteFile(f.path, f.data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				compressFixture(t, path, true)
			}
			next := NewHistory(resolveFixture(t, home, locateChat))
			defer next.Close()
			if mode, ok := next.RestoreBest(context.Background(), cache); !ok || mode != "prefix" {
				t.Fatal("representation checkpoint not reused")
			}
			if _, err := next.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !next.State.Snapshot().Equal(want) {
				t.Fatal("representation transition changed accounting")
			}
		})
	}
}

func TestAppendDuringChunkLoadIsCaughtUpWithoutNotification(t *testing.T) {
	home := t.TempDir()
	first := fixtureTurn("a", 1000, 1000, 100)
	all := append(first, fixtureTurn("b", 10000, 1000, 200)...)
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, all...)
	h := resolveFixture(t, home, locateChat)
	want := readFixture(t, h).State.Snapshot()
	cut := f.offsets[1+len(first)]
	if err := os.WriteFile(f.path, f.data[:cut], 0600); err != nil {
		t.Fatal(err)
	}
	r := NewHistory(h)
	defer r.Close()
	ctx := &auditMutatingContext{Context: context.Background(), reader: r, mutate: func() {
		out, err := os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = out.Write(f.data[cut:])
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := r.UpdateChunk(ctx); err != nil {
		t.Fatal(err)
	}
	if !ctx.fired || !r.Loading() {
		t.Fatal("growth during snapshot was reported as ready")
	}
	for r.Loading() {
		if _, err := r.UpdateChunk(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !r.State.Snapshot().Equal(want) {
		t.Fatal("missed append without a new directory notification")
	}
}

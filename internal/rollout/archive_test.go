package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func compressFixture(t testing.TB, path string, remove bool) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	data := e.EncodeAll(raw, nil)
	e.Close()
	out := path + ".zst"
	if err := os.WriteFile(out, data, 0600); err != nil {
		t.Fatal(err)
	}
	if remove {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestCompressedHistoryBoundaryRecoveryAndCheckpoint(t *testing.T) {
	for _, headCompressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain_head", true: "compressed_head"}[headCompressed], func(t *testing.T) {
			home := t.TempDir()
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			ref := root.ref(len(root.offsets) - 1)
			head := fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref, fixtureTurn("b", 10000, 2000, 200)...)
			want := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
			if err := os.WriteFile(root.path, []byte(strings.ReplaceAll(string(root.data), `"type":`, `"type" : `)), 0600); err != nil {
				t.Fatal(err)
			}
			compressFixture(t, root.path, true)
			if headCompressed {
				compressFixture(t, head.path, true)
			}
			bc := &BoundaryCache{}
			locator := fixtureLocator{Home: home, Boundaries: bc}
			h, err := locator.Resolve(context.Background(), locateChat)
			if err != nil {
				t.Fatal(err)
			}
			r := readFixture(t, h)
			if !r.State.Snapshot().Equal(want) || r.RecoveredBoundaries() != 1 {
				t.Fatal("compressed history changed accounting")
			}
			cache := filepath.Join(t.TempDir(), "checkpoint.json")
			if err := r.SaveCheckpoint(cache); err != nil {
				t.Fatal(err)
			}
			next := NewHistory(h)
			defer next.Close()
			if !next.RestoreCheckpoint(cache) {
				t.Fatal("compressed checkpoint rejected")
			}
			if changed, err := next.Update(context.Background()); err != nil || changed || !next.State.Snapshot().Equal(want) {
				t.Fatalf("compressed checkpoint mismatch: %v", err)
			}
			mapping := filepath.Join(t.TempDir(), "sources.json")
			if err := bc.Save(mapping); err != nil {
				t.Fatal(err)
			}
			loaded := &BoundaryCache{}
			if !loaded.Load(mapping) {
				t.Fatal("archive metadata did not persist")
			}
			fresh, err := (fixtureLocator{Home: home, Boundaries: loaded}).Resolve(context.Background(), locateChat)
			if err != nil || fresh.key() != h.key() {
				t.Fatalf("source mapping changed: %v", err)
			}
		})
	}
}

func TestCompressedHistoryCorruptionCancellationAndSiblingRules(t *testing.T) {
	for _, mode := range []string{"checksum", "truncated", "cancelled", "plain_sibling"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			z := compressFixture(t, f.path, mode != "plain_sibling")
			b, err := os.ReadFile(z)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "truncated" {
				b = b[:len(b)-3]
			} else {
				b[len(b)-1] ^= 1
			}
			if err := os.WriteFile(z, b, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			h, err := (fixtureLocator{Home: home}).Resolve(ctx, locateChat)
			if mode == "plain_sibling" {
				if err != nil || h.Head != f.path {
					t.Fatalf("native plain sibling precedence lost: %v", err)
				}
				return
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrArchiveInvalid) {
				t.Fatalf("damaged archive accepted: %v", err)
			}
		})
	}
}

func TestRecoveryCopyRequiresEquivalentAccounting(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "equivalent", true: "conflicting"}[conflict], func(t *testing.T) {
			home := t.TempDir()
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			base := root.ref(len(root.offsets) - 1)
			compressFixture(t, root.path, false)
			dir := filepath.Join(home, "sessions", "recovered")
			os.MkdirAll(dir, 0700)
			plain := filepath.Join(dir, filepath.Base(root.path))
			if err := os.Rename(root.path, plain); err != nil {
				t.Fatal(err)
			}
			if conflict {
				data := strings.ReplaceAll(string(root.data), `"output_tokens":100`, `"output_tokens":900`)
				os.WriteFile(plain, []byte(data), 0600)
			}
			head := fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base)
			h, err := (Locator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat)
			if err == nil {
				r := NewHistory(h)
				defer r.Close()
				_, err = r.Update(context.Background())
				if !conflict && (r.State.StepCount != 1 || r.State.TurnCount != 1) {
					t.Fatal("representation was double counted")
				}
			}
			if conflict && !errors.Is(err, ErrLogAmbiguous) {
				t.Fatal("conflicting ancestor accepted", err)
			}
			if !conflict && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUppercaseArchiveAndConcatenatedFrames(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	want := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
	e, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	cut := f.offsets[3]
	data := e.EncodeAll(f.data[:cut], nil)
	data = append(data, e.EncodeAll(f.data[cut:], nil)...)
	e.Close()
	if err := os.WriteFile(f.path+".ZST", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	selectFixture(home, locateChat, f.path+".ZST")
	r := readFixture(t, resolveFixture(t, home, locateChat))
	if !r.State.Snapshot().Equal(want) {
		t.Fatal("uppercase or concatenated archive changed accounting")
	}
}

func TestCompressedHeadCannotCertifyUnfinishedRecord(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	if err := os.WriteFile(f.path, f.data[:len(f.data)-1], 0600); err != nil {
		t.Fatal(err)
	}
	compressFixture(t, f.path, true)
	r := NewHistory(resolveFixture(t, home, locateChat))
	defer r.Close()
	if _, err := r.Update(context.Background()); !errors.Is(err, ErrArchiveInvalid) || r.State.Snapshot().HistoryComplete {
		t.Fatalf("unfinished archived record certified complete: %v", err)
	}
}

func TestCachedCopyProofCannotCoverAnUnseenRewrite(t *testing.T) {
	home := t.TempDir()
	records := fixtureTurn("a", 1000, 1000, 100)
	records = append(records, testRecord{"response_item", map[string]any{"padding": strings.Repeat("x", 200000)}, 9000})
	records = append(records, fixtureTurn("b", 10000, 1000, 200)...)
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	compressFixture(t, f.path, false)
	dir := filepath.Join(home, "sessions", "recovered")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, filepath.Base(f.path))
	if err := os.Rename(f.path, plain); err != nil {
		t.Fatal(err)
	}
	base := f.ref(len(f.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base)
	h := resolveFixture(t, home, locateChat)
	readFixture(t, h)
	r := NewHistory(h)
	defer r.Close()
	ctx := &auditMutatingContext{Context: context.Background(), reader: r, mutate: func() {
		updated := strings.ReplaceAll(string(f.data), `"output_tokens":200`, `"output_tokens":900`)
		if err := os.WriteFile(plain, []byte(updated), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := r.Update(ctx); !ctx.fired || err == nil {
		t.Fatal("cached equivalence certified newly changed, unread data")
	}
	if _, err := r.Update(context.Background()); !errors.Is(err, ErrLogAmbiguous) {
		t.Fatalf("changed copy was not checked: %v", err)
	}
}

func TestArchiveDecoderPoolIsDrainedAfterReadAndClose(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	compressFixture(t, f.path, true)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	r.Close()
	decoderReuse.Lock()
	n := len(decoderReuse.idle)
	decoderReuse.Unlock()
	if n != 0 {
		t.Fatal("decoded history buffers retained while idle")
	}
}

package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
)

func TestDesktopSelectedNewRootExcludesRevokedResponsesAndCache(t *testing.T) {
	home := t.TempDir()
	old := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("revoked", 1000, 1000, 999)...)
	root := fixtureHistory(t, home, locateChat, locateOther, 0, nil, fixtureTurn("kept-a", 10000, 2000, 200)...)
	head := fixtureHistory(t, home, locateChat, locateThird, *root.ref(len(root.offsets) - 1).Ordinal, root.ref(len(root.offsets)-1), fixtureTurn("kept-b", 20000, 4000, 400)...)
	// Neither time nor the stale database entry is permitted to pick the branch.
	if err := os.Chtimes(old.path, time.Now().Add(24*time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	l := fixtureLocator{Home: home}
	if _, err := (Locator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrBranchUnverified) {
		t.Fatal("unproved branch selected", err)
	}
	var stream live.Stream
	stream.Reset(locateChat, "local", "monitor")
	request := stream.Discover().(map[string]any)
	stream.Apply(&ipc.Frame{Type: "response", Method: "thread-owner-discovery", RequestID: request["requestId"].(string), ResultType: "success", HandledBy: "owner"})
	apply := func(revision int64, path string) {
		b, _ := json.Marshal(map[string]any{"type": "broadcast", "method": "thread-stream-state-changed", "sourceClientId": "owner", "targetClientIds": []string{"monitor"}, "version": 11,
			"params": map[string]any{"conversationId": locateChat, "hostId": "local", "change": map[string]any{"type": "snapshot", "revision": revision, "conversationState": map[string]any{"rolloutPath": path}}}})
		f, err := ipc.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		stream.Apply(f)
		l.ActivePath = stream.View().RolloutPath
	}
	cache := t.TempDir()
	read := func() *Reader {
		h, err := l.Resolve(context.Background(), locateChat)
		if err != nil {
			t.Fatal(err)
		}
		return readFixture(t, h)
	}
	apply(1, old.path)
	r := read()
	if err := r.SaveCheckpoint(checkpointPath(cache, old.path)); err != nil {
		t.Fatal(err)
	}
	apply(2, root.path)
	if s := read().State.Snapshot(); s.Steps != 1 || s.DecodeTokens != 200 {
		t.Fatal("a discovered descendant overrode Desktop's exact selection")
	}
	apply(3, head.path)
	h, err := l.Resolve(context.Background(), locateChat)
	if err != nil || len(h.Segments) != 2 || h.Head != head.path {
		t.Fatalf("selected new branch: %+v %v", h, err)
	}
	r = NewHistory(h)
	defer r.Close()
	_, _ = r.RestoreBest(context.Background(), cache)
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := r.State.Snapshot()
	if s.Turns != 2 || s.Steps != 2 || s.DecodeSamples != 2 || s.DecodeTokens != 600 || s.DecodeMS != 6000 || s.TPS == nil || math.Abs(*s.TPS-100) > 1e-9 || s.ModelMS != 4500 || s.ToolMS != 600 || s.TTFTCount != 2 || s.AverageTTFT == nil || *s.AverageTTFT != 200 || !s.HistoryComplete || !s.CountsComplete {
		t.Fatalf("revoked history contributed to statistics: %+v", s)
	}
	if _, err := os.Stat(old.path); err != nil {
		t.Fatal("old source was changed", err)
	}
	// Selecting the empty replacement root immediately clears old statistics.
	empty := fixtureHistory(t, home, locateChat, "01a15e35-dabf-7521-bd4c-a15b0df6694c", 0, nil)
	apply(4, empty.path)
	s = read().State.Snapshot()
	if s.Steps != 0 || s.Turns != 0 || s.TPS != nil || s.DecodeSamples != 0 || s.DecodeTokens != 0 {
		t.Fatal("empty revert retained revoked statistics")
	}
}

func TestExplicitBranchMustVerifyOwnershipSourceAndAncestors(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("kept", 1000, 1000, 100)...)
	head := fixtureHistory(t, home, locateChat, locateOther, *root.ref(7).Ordinal, root.ref(7))
	foreign := fixtureHistory(t, home, locateThird, locateThird, 0, nil)
	for _, tc := range []struct {
		path string
		want error
	}{
		{foreign.path, ErrChatMismatch}, {filepath.Join(home, "missing.jsonl"), ErrLogIdentityUnverified}, {"relative.jsonl", ErrLogPathUnsupported},
	} {
		_, err := (fixtureLocator{Home: home, ActivePath: tc.path}).Resolve(context.Background(), locateChat)
		if !errors.Is(err, tc.want) {
			t.Fatalf("source %q: %v want %v", tc.path, err, tc.want)
		}
	}
	// A current head is authority for that branch, not permission to bypass an
	// ambiguous physical parent or malformed inherited boundary.
	putLog(t, home, "archived_sessions/"+filepath.Base(root.path), string(root.data))
	if _, err := (fixtureLocator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrLogAmbiguous) {
		t.Fatal("conflicting ancestor accepted", err)
	}
}

func TestExplicitBranchAddressIsNeverLexicallyRewritten(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("kept", 1000, 1000, 100)...)
	dir, name := filepath.Dir(f.path), filepath.Base(f.path)
	// Cleaning first would bypass the missing component and read a different
	// address. The file API, not filepath.Clean, determines source identity.
	missing := dir + string(os.PathSeparator) + "missing" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + name
	if runtime.GOOS == "windows" {
		// Ordinary Win32 paths collapse "missing\\.." before the file API sees
		// it. A verbatim address retains those components as supplied.
		switch {
		case strings.HasPrefix(missing, `\\?\`):
		case len(filepath.VolumeName(missing)) == 2:
			missing = `\\?\` + missing
		case strings.HasPrefix(missing, `\\`):
			missing = `\\?\UNC\` + strings.TrimPrefix(missing, `\\`)
		default:
			t.Fatalf("unsupported native test volume: %q", filepath.VolumeName(missing))
		}
	}
	if _, err := (fixtureLocator{Home: home, ActivePath: missing}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrLogNotFound) {
		t.Fatalf("unavailable explicit address was rewritten: %v", err)
	}
	raw := dir + string(os.PathSeparator) + "." + string(os.PathSeparator) + name
	h, err := (fixtureLocator{Home: home, ActivePath: raw}).Resolve(context.Background(), locateChat)
	if err != nil || h.Head != raw || len(h.Segments) != 1 || h.Segments[0].Path != raw {
		t.Fatalf("explicit source address changed: %+v %v", h, err)
	}
	if s := readFixture(t, h).State.Snapshot(); s.Turns != 1 || s.Steps != 1 || s.DecodeTokens != 100 || !s.HistoryComplete {
		t.Fatalf("source path preservation changed accounting: %+v", s)
	}
}

package win

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

const resolverChat = "01a15da4-16e9-7951-8fce-50f7bef8bb83"
const physicalChild = "01a15e35-dabf-7521-bd4c-a15b0df6694a"

func nativeRecord(t *testing.T, ordinal int64, kind string, payload any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"ordinal": ordinal, "type": kind, "timestamp": "2026-10-24T10:00:00Z", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func nativeLog(t *testing.T, home, logical, physical string, ordinal int64, base map[string]any, records ...[]byte) (string, []byte) {
	t.Helper()
	name := "rollout-2026-10-24T10-00-00-" + logical
	if physical != logical {
		name += "_" + physical
	}
	p := filepath.Join(home, "sessions", name+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"id": logical, "history_mode": "paginated"}
	if base != nil {
		meta["history_base"] = base
	}
	b := nativeRecord(t, ordinal, "session_meta", meta)
	for _, r := range records {
		b = append(b, r...)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p, b
}

func nativeUsage(t *testing.T, ordinal int64, turn, response string, output, totalOutput int64) []byte {
	totalInput := int64(100)
	if ordinal != 1 {
		totalInput = 200
	}
	return nativeRecord(t, ordinal, "token_usage_record", map[string]any{
		"turn_id": turn, "response_id": response,
		"usage":              map[string]any{"input_tokens": 100, "output_tokens": output},
		"thread_token_usage": map[string]any{"input_tokens": totalInput, "output_tokens": totalOutput},
	})
}

// Real Windows file I/O through both native and /mnt/<drive>/ representations;
// no backend configuration, registry access or filesystem discovery is involved.
func TestNativeWindowsLogSourceUsesDesktopHeadAndIgnoresIndexOrMtime(t *testing.T) {
	home := filepath.Join(t.TempDir(), "AI 数据")
	old, _ := nativeLog(t, home, resolverChat, resolverChat, 0, nil)
	current, _ := nativeLog(t, home, resolverChat, physicalChild, 0, nil)
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(old, future, future); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(home, "state_5.sqlite")
	if err := os.WriteFile(db, []byte("stale/invalid index must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(db)
	paths := []string{current, strings.ReplaceAll(current, `\`, "/")}
	volume := filepath.VolumeName(current)
	if len(volume) == 2 && volume[1] == ':' {
		paths = append(paths, "/mnt/"+strings.ToLower(volume[:1])+strings.ReplaceAll(current[2:], `\`, "/"))
	}
	for _, raw := range paths {
		h, p, err := resolveActiveThread(context.Background(), `Z:\unrelated-data-home`, "local", resolverChat, raw, nil)
		if err != nil || h.Head != current || len(h.Segments) != 1 || p.Home != home {
			t.Fatalf("%+v %+v %v", h, p, err)
		}
	}
	after, _ := os.ReadFile(db)
	if !bytes.Equal(before, after) {
		t.Fatal("source lookup changed the index")
	}
	if _, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, "", nil); !errors.Is(err, rollout.ErrBranchUnverified) {
		t.Fatal("missing Desktop source guessed a file", err)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, current, nil); !errors.Is(err, rollout.ErrLogNotFound) {
		t.Fatal("missing source fell back to old root", err)
	}
}

func TestNativeWindowsLogSourceVerifiesChatAndHostBeforeReading(t *testing.T) {
	home := t.TempDir()
	foreign, _ := nativeLog(t, home, physicalChild, physicalChild, 0, nil)
	if _, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, foreign, nil); !errors.Is(err, rollout.ErrChatMismatch) {
		t.Fatal("foreign chat accepted", err)
	}
	if _, _, err := resolveActiveThread(context.Background(), home, "remote-peer", resolverChat, foreign, nil); !errors.Is(err, source.ErrHostUnsupported) {
		t.Fatal("remote identity reused local path", err)
	}
	if _, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, "/home/user/.codex/sessions/unknown.jsonl", nil); !errors.Is(err, rollout.ErrLogPathUnsupported) {
		t.Fatal("non-Windows source inferred", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolveActiveThread(ctx, home, "local", resolverChat, foreign, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeWindowsLogSourcePreservesHistoryBoundaryAndExcludesRevertedResponse(t *testing.T) {
	home := t.TempDir()
	first := nativeUsage(t, 1, "turn-1", "response-1", 5, 5)
	reverted := nativeUsage(t, 2, "reverted-turn", "reverted-response", 99999, 100004)
	parent, original := nativeLog(t, home, resolverChat, resolverChat, 0, nil, first, reverted)
	cut := int64(len(original) - len(reverted))
	base := map[string]any{"thread_id": resolverChat, "end_ordinal_exclusive": int64(2), "end_byte_offset": cut}
	head, _ := nativeLog(t, home, resolverChat, physicalChild, 2, base, nativeUsage(t, 3, "turn-2", "response-2", 3, 8))
	h, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, head, nil)
	if err != nil || len(h.Segments) != 2 || h.Segments[0].Bytes != cut {
		t.Fatal(h, err)
	}
	r := rollout.NewHistory(h)
	if _, err = r.Update(context.Background()); err != nil {
		r.Close()
		t.Fatal(err)
	}
	snapshot := r.State.Snapshot()
	r.Close()
	if snapshot.Turns != 2 || snapshot.Steps != 2 || !snapshot.HasUsage || snapshot.Usage.Output != 8 {
		t.Fatalf("reverted response entered statistics: %+v", snapshot)
	}
	after, _ := os.ReadFile(parent)
	if !bytes.Equal(after, original) {
		t.Fatal("replay changed the source log")
	}
	badBase := map[string]any{"thread_id": resolverChat, "end_ordinal_exclusive": int64(2), "end_byte_offset": cut + int64(len(reverted))}
	nativeLog(t, home, resolverChat, physicalChild, 2, badBase, nativeUsage(t, 3, "turn-2", "response-2", 3, 8))
	if _, _, err = resolveActiveThread(context.Background(), home, "local", resolverChat, head, nil); !errors.Is(err, rollout.ErrHistoryBoundary) {
		t.Fatal("conflicting reference boundary accepted", err)
	}
}

package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testRecord struct {
	kind    string
	payload map[string]any
	ms      int64
}
type historyFixture struct {
	path, physical, logical string
	start                   int64
	data                    []byte
	offsets                 []int64
	records                 []testRecord
}

func fixtureHistory(t testing.TB, home, logical, physical string, start int64, base *historyBase, records ...testRecord) historyFixture {
	t.Helper()
	payload := map[string]any{"id": logical, "session_id": logical, "history_mode": "paginated"}
	if base != nil {
		payload["history_base"] = base
	}
	all := append([]testRecord{{"session_meta", payload, 1}}, records...)
	f := historyFixture{physical: physical, logical: logical, start: start, offsets: []int64{0}, records: records}
	for i, r := range all {
		line, err := json.Marshal(map[string]any{"ordinal": start + int64(i), "type": r.kind, "payload": r.payload, "timestamp": time.UnixMilli(r.ms).UTC().Format(time.RFC3339Nano)})
		if err != nil {
			t.Fatal(err)
		}
		f.data = append(f.data, line...)
		f.data = append(f.data, '\n')
		f.offsets = append(f.offsets, int64(len(f.data)))
	}
	name := logical
	if logical != physical {
		name += "_" + physical
	}
	f.path = putLog(t, home, "sessions/2026/10/21/"+logName(name), string(f.data))
	return f
}

func (f historyFixture) ref(records int) *historyBase {
	ordinal := f.start + int64(records)
	n := f.offsets[records]
	return &historyBase{RolloutID: f.physical, Ordinal: &ordinal, Bytes: &n}
}

func fixtureTurn(id string, start, duration, output int64) []testRecord {
	return []testRecord{
		{"event_msg", map[string]any{"type": "task_started", "turn_id": id}, start},
		{"event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]any{"type": "Reasoning"}, "started_at_ms": start, "completed_at_ms": start + duration/2}, start + duration/2},
		{"event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]any{"type": "AgentMessage"}, "started_at_ms": start + duration/4, "completed_at_ms": start + duration*3/4}, start + duration*3/4},
		{"token_usage_record", map[string]any{"turn_id": id, "response_id": "response-" + id, "usage": map[string]any{"input_tokens": 100, "cached_input_tokens": 50, "output_tokens": output}, "thread_token_usage": map[string]any{"input_tokens": start, "cached_input_tokens": start / 2, "output_tokens": output}}, start + duration},
		{"event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]any{"type": "CommandExecution"}, "started_at_ms": start + duration, "completed_at_ms": start + duration + 300}, start + duration + 300},
		{"event_msg", map[string]any{"type": "task_complete", "turn_id": id, "time_to_first_token_ms": 200}, start + duration + 300},
	}
}

func resolveFixture(t testing.TB, home, id string) History {
	t.Helper()
	h, err := (fixtureLocator{Home: home}).Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func readFixture(t testing.TB, h History) *Reader {
	t.Helper()
	r := NewHistory(h)
	t.Cleanup(r.Close)
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHistoryRevertBoundaryRestoresCountsTPSAndTiming(t *testing.T) {
	home := t.TempDir()
	a := fixtureTurn("a", 1000, 1000, 100)
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(a, fixtureTurn("discarded", 10000, 1000, 900)...)...)
	base := root.ref(1 + len(a))
	head := fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 20000, 2000, 200)...)
	// Neither a stale index nor a newer timestamp on the discarded source can
	// overrule the only metadata leaf.
	if err := os.Chtimes(root.path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	l := fixtureLocator{Home: home}
	h, err := l.Resolve(context.Background(), locateChat)
	if err != nil || h.Head != head.path || len(h.Segments) != 2 || h.Segments[0].Bytes != *base.Bytes {
		t.Fatalf("wrong lineage: %+v %v", h, err)
	}
	r := readFixture(t, h)
	s := r.State.Snapshot()
	if s.Turns != 2 || s.Steps != 2 || !s.CountsComplete || !s.HistoryComplete || s.DecodeSamples != 2 || s.DecodeSkipped != 0 || s.DecodeMS != 3000 || s.DecodeTokens != 300 || s.TPS == nil || math.Abs(*s.TPS-100) > 1e-9 || s.ModelMS != 2250 || s.ToolMS != 600 || s.TTFTCount != 2 || s.AverageTTFT == nil || *s.AverageTTFT != 200 {
		t.Fatalf("wrong effective-history statistics: %+v", s)
	}
	if s.Total != 20200 {
		t.Fatalf("summed cumulative usage across segments: %d", s.Total)
	}
}

func TestHistoryThreeLevelsArchiveAndPhysicalReference(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	b := root.ref(len(root.offsets) - 1)
	mid := fixtureHistory(t, home, locateChat, locateOther, *b.Ordinal, b, fixtureTurn("b", 10000, 1000, 100)...)
	b = mid.ref(len(mid.offsets) - 1)
	head := fixtureHistory(t, home, locateChat, locateThird, *b.Ordinal, b, fixtureTurn("c", 20000, 1000, 100)...)
	archive := filepath.Join(home, "archived_sessions", filepath.Base(root.path))
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root.path, archive); err != nil {
		t.Fatal(err)
	}
	h := resolveFixture(t, home, locateChat)
	if h.Head != head.path || len(h.Segments) != 3 || h.Segments[0].Path != archive {
		t.Fatal("physical ancestor chain not resolved")
	}
	s := readFixture(t, h).State.Snapshot()
	if s.Turns != 3 || s.Steps != 3 || !s.HistoryComplete {
		t.Fatalf("nested history lost: %+v", s)
	}
}

func TestHistoryCompetingLeavesCannotOverrideExplicitSelection(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil)
	base := root.ref(1)
	a := fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base)
	fixtureHistory(t, home, locateChat, locateThird, *base.Ordinal, base)
	for _, path := range []string{root.path, a.path} {
		h, err := (Locator{Home: home, ActivePath: path}).Resolve(context.Background(), locateChat)
		if err != nil || h.Head != path {
			t.Fatal("explicit branch ignored", err)
		}
	}
}

func TestHistoryBoundaryMustMatchBytesAndOrdinals(t *testing.T) {
	for _, kind := range []string{"conflicting record boundary", "wrong ordinal", "before start", "missing field"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			b := root.ref(len(root.offsets) - 1)
			switch kind {
			case "conflicting record boundary":
				*b.Bytes = root.offsets[2]
			case "wrong ordinal":
				*b.Ordinal++
			case "before start":
				*b.Ordinal = -1
			case "missing field":
				b.Bytes = nil
			}
			fixtureHistory(t, home, locateChat, locateOther, *b.Ordinal, b)
			if _, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); err == nil {
				t.Fatal("invalid boundary accepted")
			}
		})
	}
}

func TestHistoryEmptyPrefixCycleAndMissingAncestor(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("discarded", 1000, 1000, 100)...)
	b := root.ref(0)
	head := fixtureHistory(t, home, locateChat, locateOther, 0, b, fixtureTurn("selected", 10000, 1000, 100)...)
	s := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
	if s.Turns != 1 || !s.HistoryComplete {
		t.Fatal("empty referenced prefix inherited discarded data")
	}
	if err := os.Remove(root.path); err != nil {
		t.Fatal(err)
	}
	if _, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrHistoryMissing) {
		t.Fatalf("missing base hidden: %v", err)
	}
	if err := os.Remove(head.path); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	fixtureHistory(t, home, locateChat, locateChat, 0, &historyBase{RolloutID: locateOther, Ordinal: &zero, Bytes: &zero})
	fixtureHistory(t, home, locateChat, locateOther, 0, &historyBase{RolloutID: locateChat, Ordinal: &zero, Bytes: &zero})
	if _, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrHistoryCycle) {
		t.Fatalf("cycle accepted: %v", err)
	}
}

func TestHistoryRecordReversalCannotClaimCompletenessOrRecoverOnAppend(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	broken := strings.Replace(string(root.data), `"ordinal":2`, `"ordinal":9`, 1)
	if err := os.WriteFile(root.path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewHistory(resolveFixture(t, home, locateChat))
	defer r.Close()
	if _, err := r.Update(context.Background()); !errors.Is(err, ErrHistoryOrdinal) || r.State.Snapshot().HistoryComplete {
		t.Fatal("ordinal reversal claimed a complete history")
	}
	appendFile(t, root.path, "\n")
	if _, err := r.Update(context.Background()); !errors.Is(err, ErrHistoryOrdinal) {
		t.Fatal("append hid sequence error")
	}
	if err := os.WriteFile(root.path, root.data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background()); err != nil || !r.State.Snapshot().HistoryComplete || r.State.Snapshot().Turns != 1 {
		t.Fatalf("source repair failed: %v", err)
	}
}

func TestHistoryDeduplicatesOldResponsesAndTimingAfterEviction(t *testing.T) {
	home := t.TempDir()
	var records []testRecord
	for i := range 40 {
		records = append(records, fixtureTurn(fmt.Sprint(i), 1000+int64(i)*5000, 1000, 100)...)
	}
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	b := root.ref(len(root.offsets) - 1)
	// Replayed events retain their turn/response identity but get new ordinals.
	duplicated := append(append([]testRecord{}, fixtureTurn("0", 1000, 1000, 100)...), fixtureTurn("new", 300000, 1000, 100)...)
	fixtureHistory(t, home, locateChat, locateOther, *b.Ordinal, b, duplicated...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	s := r.State.Snapshot()
	if s.Turns != 41 || s.Steps != 41 || s.DecodeSamples != 41 || s.DecodeMS != 41000 || s.ModelMS != 30750 || s.ToolMS != 12300 || s.TTFTCount != 41 || !s.CountsComplete {
		t.Fatalf("duplicate old history changed totals: %+v", s)
	}
	if r.seenIndex == nil {
		t.Fatal("duplicate witness never checked exact source")
	}
	f := r.seenIndex.file
	r.Close()
	requireClosedFile(t, f)
}

func TestHistoryCheckpointAppendAndAncestorRewrite(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	b := root.ref(len(root.offsets) - 1)
	head := fixtureHistory(t, home, locateChat, locateOther, *b.Ordinal, b, fixtureTurn("b", 10000, 1000, 100)...)
	h := resolveFixture(t, home, locateChat)
	r := readFixture(t, h)
	cache := filepath.Join(t.TempDir(), "cache.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	restored := NewHistory(h)
	defer restored.Close()
	if !restored.RestoreCheckpoint(cache) {
		t.Fatal("whole-history cache rejected")
	}
	more := fixtureHistory(t, t.TempDir(), locateChat, locateOther, head.start+int64(len(head.offsets)-1), nil, fixtureTurn("c", 20000, 1000, 100)...)
	appendFile(t, head.path, string(more.data[more.offsets[1]:])) // Skip fixture metadata, preserve next ordinal below.
	// The helper's extra metadata consumes one ordinal. Correct the appended range.
	data, _ := os.ReadFile(head.path)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := len(head.offsets) - 1; i < len(lines); i++ {
		var row map[string]any
		json.Unmarshal([]byte(lines[i]), &row)
		row["ordinal"] = head.start + int64(i)
		raw, _ := json.Marshal(row)
		lines[i] = string(raw)
	}
	if err := os.WriteFile(head.path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Update(context.Background()); err != nil || restored.State.Snapshot().Turns != 3 {
		t.Fatalf("incremental chain update: %v", err)
	}
	full := readFixture(t, h)
	if !restored.State.Snapshot().Equal(full.State.Snapshot()) {
		t.Fatal("restored and full history disagree")
	}
	changed := strings.Replace(string(root.data), `"usage":{"cached_input_tokens":50,"input_tokens":100,"output_tokens":100}`, `"usage":{"cached_input_tokens":50,"input_tokens":100,"output_tokens":900}`, 1)
	if changed == string(root.data) {
		t.Fatal("response usage fixture not changed")
	}
	if len(changed) != len(root.data) {
		t.Fatal("fixture must retain byte boundaries")
	}
	if err := os.WriteFile(root.path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if NewHistory(h).RestoreCheckpoint(cache) {
		t.Fatal("changed ancestor accepted cached totals")
	}
	if err := r.SaveCheckpoint(cache); err == nil {
		t.Fatal("stale history overwrote checkpoint")
	}
	if _, err := restored.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restored.State.DecodeTokens != 1100 {
		t.Fatalf("ancestor rewrite not replayed: %d", restored.State.DecodeTokens)
	}
}

func TestHistoryRawSegmentAndOldCheckpointNeverClaimFullChain(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil)
	b := root.ref(1)
	head := fixtureHistory(t, home, locateChat, locateOther, *b.Ordinal, b, fixtureTurn("a", 1000, 1000, 100)...)
	r := New(head.path)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.State.Snapshot().HistoryComplete || r.State.Snapshot().CountsComplete {
		t.Fatal("standalone continuation presented complete history")
	}
	cache := filepath.Join(t.TempDir(), "cache.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	chain := NewHistory(resolveFixture(t, home, locateChat))
	defer chain.Close()
	if chain.RestoreCheckpoint(cache) {
		t.Fatal("single-file checkpoint reused for history chain")
	}
	data, _ := os.ReadFile(cache)
	data = []byte(strings.Replace(string(data), checkpointFormat, "cum-history-sha256", 1))
	os.WriteFile(cache, data, 0600)
	if chain.RestoreCheckpoint(cache) {
		t.Fatal("previous cache format accepted")
	}
}

func TestHistoryUnsequencedSourceCannotClaimComplete(t *testing.T) {
	home := t.TempDir()
	putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat)+usageLine)
	s := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
	if s.HistoryComplete || s.CountsComplete {
		t.Fatal("unsequenced source claimed a fully verified history")
	}
}

func TestHistoryCancellationDoesNotLeakOpenSources(t *testing.T) {
	home := t.TempDir()
	fixtureHistory(t, home, locateChat, locateChat, 0, nil)
	h := resolveFixture(t, home, locateChat)
	r := NewHistory(h)
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Update(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled replay continued")
	}
	if _, err := (fixtureLocator{Home: home}).Resolve(ctx, locateChat); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled discovery continued")
	}
	f, err := r.openSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	part := f.(*historyFile).files[0]
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosedFile(t, part)
}

func TestHistoryOversizedAndMalformedRecordsKeepHonestLowerBounds(t *testing.T) {
	for _, oversized := range []bool{true, false} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			home := t.TempDir()
			turn := fixtureTurn("a", 1000, 1000, 100)
			body := "placeholder"
			if oversized {
				body = strings.Repeat("x", MaxRecordBytes)
			}
			records := append([]testRecord{turn[0], {"response_item", map[string]any{"text": body}, 1100}}, turn[1:]...)
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
			if !oversized {
				data := append([]byte{}, f.data[:f.offsets[2]]...)
				data = append(data, []byte("invalid JSON\n")...)
				data = append(data, f.data[f.offsets[3]:]...)
				if err := os.WriteFile(f.path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := readFixture(t, resolveFixture(t, home, locateChat))
			s := r.State.Snapshot()
			if s.Steps != 1 || s.Turns != 1 || s.HistoryComplete || s.CountsComplete || s.TPS != nil {
				t.Fatalf("damaged history lost useful counts or claimed complete timing: %+v", s)
			}
			if oversized && s.Oversized != 1 || !oversized && s.Malformed != 1 {
				t.Fatal("damage diagnostic lost")
			}
		})
	}
}

func TestHistoryExplicitForkDoesNotChangeParentOrSumUsageBaselines(t *testing.T) {
	home := t.TempDir()
	a := fixtureTurn("a", 1000, 1000, 100)
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(a, fixtureTurn("parent-tail", 10000, 1000, 100)...)...)
	b := root.ref(1 + len(a))
	child := fixtureTurn("child", 20000, 1000, 10)
	child[3].payload["thread_token_usage"] = map[string]any{"input_tokens": 50, "cached_input_tokens": 20, "output_tokens": 10}
	fixtureHistory(t, home, locateOther, locateOther, *b.Ordinal, b, child...)
	parent := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
	if parent.Turns != 2 || parent.Steps != 2 {
		t.Fatal("foreign fork truncated selected parent's history")
	}
	fork := readFixture(t, resolveFixture(t, home, locateOther)).State.Snapshot()
	if fork.ThreadID != locateOther || fork.Turns != 2 || fork.Steps != 2 || fork.Total != 60 || fork.DecodeTokens != 110 || !fork.HistoryComplete {
		t.Fatalf("explicitly inherited prefix mixed usage baselines: %+v", fork)
	}
}

func TestHistoryDepthLimitIsExplicit(t *testing.T) {
	home := t.TempDir()
	last := fixtureHistory(t, home, locateChat, locateChat, 0, nil)
	for i := range maxHistorySegments {
		base := last.ref(1)
		physical := fmt.Sprintf("00000000-0000-7000-8000-%012d", i)
		last = fixtureHistory(t, home, locateChat, physical, *base.Ordinal, base)
	}
	if _, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrHistoryCycle) {
		t.Fatalf("over-budget chain accepted: %v", err)
	}
}

func TestTrackedHistoryRotationAndSameHeadReferenceChangeRebuildCache(t *testing.T) {
	home := t.TempDir()
	a := fixtureTurn("a", 1000, 1000, 100)
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(a, fixtureTurn("b", 10000, 1000, 100)...)...)
	l := fixtureLocator{Home: home}
	tr := Tracked{ID: locateChat, CacheDir: t.TempDir(), Resolve: func() (History, error) { return l.Resolve(context.Background(), locateChat) }}
	defer func() {
		if tr.Reader != nil {
			tr.Reader.Close()
		}
	}()
	now := time.Unix(100, 0)
	if _, _, err := tr.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if tr.Reader.State.Snapshot().Turns != 2 {
		t.Fatal("bad initial history")
	}
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	base := root.ref(1 + len(a))
	head := fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("c", 20000, 1000, 100)...)
	if _, moved, err := tr.Update(context.Background(), now.Add(31*time.Second)); err != nil || !moved || tr.Reader.Path != head.path || tr.Reader.State.Snapshot().Turns != 2 {
		t.Fatalf("rotation: %v", err)
	}
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	oldCache, _ := os.ReadFile(tr.CachePath)
	base = root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("c", 20000, 1000, 100)...)
	if _, moved, err := tr.Update(context.Background(), now.Add(62*time.Second)); err != nil || !moved || tr.Reader.State.Snapshot().Turns != 3 {
		t.Fatalf("same head's changed reference reused old counts: %v", err)
	}
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	newCache, _ := os.ReadFile(tr.CachePath)
	if string(oldCache) == string(newCache) {
		t.Fatal("cache not rebuilt")
	}
}

func TestHistoryCacheRestoresDedupForRetiredResponse(t *testing.T) {
	home := t.TempDir()
	var all []testRecord
	for i := range 40 {
		all = append(all, fixtureTurn(fmt.Sprint(i), 1000+int64(i)*5000, 1000, 100)...)
	}
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, all...)
	h := resolveFixture(t, home, locateChat)
	r := readFixture(t, h)
	cache := filepath.Join(t.TempDir(), "cache.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	restored := NewHistory(h)
	defer restored.Close()
	if !restored.RestoreCheckpoint(cache) {
		t.Fatal("cache restore failed")
	}
	next := fixtureHistory(t, t.TempDir(), locateChat, locateChat, int64(len(root.offsets)-2), nil, fixtureTurn("0", 1000, 1000, 100)...)
	appendFile(t, root.path, string(next.data[next.offsets[1]:]))
	if _, err := restored.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := restored.State.Snapshot()
	if s.Turns != 40 || s.Steps != 40 || s.DecodeSamples != 40 || !s.CountsComplete {
		t.Fatalf("restored cache forgot old response identities: %+v", s)
	}
	data, _ := os.ReadFile(cache)
	var c map[string]any
	json.Unmarshal(data, &c)
	delete(c, "Seen")
	data, _ = json.Marshal(c)
	os.WriteFile(cache, data, 0600)
	probe := NewHistory(h)
	defer probe.Close()
	if probe.RestoreCheckpoint(cache) {
		t.Fatal("incomplete dedup checkpoint accepted")
	}
}

func BenchmarkHistoryReplayAndIdle(b *testing.B) {
	home := b.TempDir()
	var a, c []testRecord
	for i := range 100 {
		a = append(a, fixtureTurn(fmt.Sprint(i), 1000+int64(i)*5000, 1000, 100)...)
		c = append(c, fixtureTurn(fmt.Sprint(i+100), 600000+int64(i)*5000, 1000, 100)...)
	}
	root := fixtureHistory(b, home, locateChat, locateChat, 0, nil, a...)
	base := root.ref(len(root.offsets) - 1)
	fixtureHistory(b, home, locateChat, locateOther, *base.Ordinal, base, c...)
	h := resolveFixture(b, home, locateChat)
	b.Run("replay_200_responses", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			r := NewHistory(h)
			if _, err := r.Update(context.Background()); err != nil {
				b.Fatal(err)
			}
			r.Close()
		}
	})
	b.Run("idle", func(b *testing.B) {
		r := readFixture(b, h)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := r.Update(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

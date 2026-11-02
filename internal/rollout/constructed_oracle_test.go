package rollout

import (
	"context"
	"fmt"
	"math"
	"testing"
)

func TestConstructedOracleAcrossRevertDuplicatesChunksAndCache(t *testing.T) {
	const turns, perTurn, steps = 200, 8, 1600
	const origin = int64(1700000000000)
	var records []testRecord
	for turn := 0; turn < turns; turn++ {
		id := fmt.Sprintf("oracle-%d", turn)
		at := origin + int64(turn)*1000
		records = append(records, testRecord{"event_msg", map[string]any{"type": "task_started", "turn_id": id}, at})
		for step := 0; step < perTurn; step++ {
			start, n := at+int64(step)*100, int64(turn*perTurn+step+1)
			for _, item := range []struct {
				kind       string
				start, end int64
			}{
				{"Reasoning", start, start + 5}, {"AgentMessage", start + 4, start + 9},
			} {
				records = append(records, testRecord{"event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]string{"type": item.kind}, "started_at_ms": item.start, "completed_at_ms": item.end}, item.end})
			}
			records = append(records, testRecord{"token_usage_record", map[string]any{"turn_id": id, "response_id": fmt.Sprint("response-", n),
				"usage":              map[string]int64{"input_tokens": 100, "cached_input_tokens": 75, "output_tokens": 10, "reasoning_output_tokens": 2},
				"thread_token_usage": map[string]int64{"input_tokens": n * 100, "cached_input_tokens": n * 75, "output_tokens": n * 10, "reasoning_output_tokens": n * 2}}, start + 10})
			for _, item := range []struct {
				kind       string
				start, end int64
			}{
				{"CommandExecution", start + 10, start + 20}, {"McpToolCall", start + 15, start + 25},
			} {
				records = append(records, testRecord{"event_msg", map[string]any{"type": "item_completed", "turn_id": id, "item": map[string]string{"type": item.kind}, "started_at_ms": item.start, "completed_at_ms": item.end}, item.end})
			}
		}
		records = append(records, testRecord{"event_msg", map[string]any{"type": "task_complete", "turn_id": id, "time_to_first_token_ms": 25}, at + 900})
	}
	// The root's extra response is revoked;
	// duplicates of the first turn arrive after its in-memory detail was evicted.
	const turnRecords = perTurn*5 + 2
	const cut = 50 * turnRecords
	home := t.TempDir()
	rootRecords := append(append([]testRecord{}, records[:cut]...), fixtureTurn("revoked", origin+9999999, 1000, 99999)...)
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, rootRecords...)
	base := root.ref(1 + cut)
	tail := append(append([]testRecord{}, records[cut:]...), records[:turnRecords]...)
	head := fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, tail...)
	h, err := (Locator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewHistory(h)
	defer reader.Close()
	for {
		if _, err := reader.UpdateChunk(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !reader.Loading() {
			break
		}
	}
	x := reader.State.Snapshot()
	// Expected totals follow directly from the constructed workload: model
	// intervals overlap for 9 ms, tools for 15 ms, decode lasts 10 ms per step.
	if x.Turns != turns || x.Steps != steps || x.Total != steps*110 || x.Usage.Cached != steps*75 || x.Usage.Reasoning != steps*2 ||
		x.ModelMS != steps*9 || x.ToolMS != steps*15 || x.DecodeTokens != steps*10 || x.DecodeMS != steps*10 || x.DecodeSamples != steps || x.DecodeSkipped != 0 ||
		x.TTFTCount != turns || x.AverageTTFT == nil || *x.AverageTTFT != 25 || x.CachePercent == nil || math.Abs(*x.CachePercent-75) > 1e-9 ||
		x.TPS == nil || *x.TPS != 1000 || !x.CountsComplete || !x.HistoryComplete || !x.TimingComplete {
		t.Fatalf("constructed oracle mismatch: %+v", x)
	}
	dir := t.TempDir()
	if err := reader.SaveCheckpoint(checkpointPath(dir, head.path)); err != nil {
		t.Fatal(err)
	}
	restored := NewHistory(h)
	defer restored.Close()
	if _, ok := restored.RestoreBest(context.Background(), dir); !ok {
		t.Fatal("oracle checkpoint rejected")
	}
	if _, err := restored.Update(context.Background()); err != nil || !restored.State.Snapshot().Equal(x) {
		t.Fatal("cache changed the oracle result", err)
	}
}

package rollout

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlausibleAggregateCorruptionCannotSurviveCheckpoint(t *testing.T) {
	d := t.TempDir()
	p, cache := filepath.Join(d, "source"), filepath.Join(d, "cache")
	if err := os.WriteFile(p, []byte(header+usageLine), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"StepCount", "TurnCount", "ModelMS", "HasCache", "HistoryComplete", "Usage"} {
		t.Run(field, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(original, &doc); err != nil {
				t.Fatal(err)
			}
			state := doc["State"].(map[string]any)
			switch field {
			case "HasCache", "HistoryComplete":
				state[field] = !state[field].(bool)
			case "Usage":
				state[field].(map[string]any)["cached_input_tokens"] = float64(70)
			default:
				state[field] = state[field].(float64) + 1
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, data, 0600); err != nil {
				t.Fatal(err)
			}
			resumed := New(p)
			defer resumed.Close()
			if resumed.RestoreCheckpoint(cache) {
				t.Fatal("accepted altered accounting over an intact source")
			}
			if _, err := resumed.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !resumed.State.Snapshot().Equal(r.State.Snapshot()) {
				t.Fatal("replay did not rebuild correct statistics")
			}
		})
	}
}

func TestCheckpointPreservesUnknownCacheMetadata(t *testing.T) {
	d := t.TempDir()
	p, cache := filepath.Join(d, "source"), filepath.Join(d, "cache")
	line := `{"type":"token_usage_record","payload":{"turn_id":"a","response_id":"r","thread_token_usage":{"input_tokens":100,"output_tokens":20}}}` + "\n"
	if err := os.WriteFile(p, []byte(header+line), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	resumed := New(p)
	defer resumed.Close()
	if !resumed.RestoreCheckpoint(cache) || !resumed.State.Snapshot().Equal(r.State.Snapshot()) || resumed.State.Snapshot().CachePercent != nil {
		t.Fatal("checkpoint changed the meaning of unknown cache metadata")
	}
}

func TestPreviousAccountingCacheIsRebuiltWithoutMigration(t *testing.T) {
	d := t.TempDir()
	p, cache := filepath.Join(d, "source"), filepath.Join(d, "cache")
	if err := os.WriteFile(p, []byte(header+usageLine), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var c checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.Version = 1
	c.Integrity, err = c.integrity()
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, b, 0600); err != nil {
		t.Fatal(err)
	}
	resumed := New(p)
	defer resumed.Close()
	if resumed.RestoreCheckpoint(cache) {
		t.Fatal("accepted the previous accounting algorithm cache")
	}
	if _, err := resumed.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !resumed.State.Snapshot().Equal(r.State.Snapshot()) {
		t.Fatal("replay failed to rebuild statistics")
	}
}

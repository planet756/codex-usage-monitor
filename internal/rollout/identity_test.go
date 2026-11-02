package rollout

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointRejectsStateFromReplacedOrRewrittenSource(t *testing.T) {
	for _, replace := range []bool{true, false} {
		t.Run(map[bool]string{true: "replacement", false: "rewrite"}[replace], func(t *testing.T) {
			d := t.TempDir()
			p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
			os.WriteFile(p, []byte(header+usageLine), 0600)
			r := New(p)
			r.Update(context.Background())
			if err := r.SaveCheckpoint(c); err != nil {
				t.Fatal(err)
			}
			good, _ := os.ReadFile(c)
			fresh := []byte(header + strings.Replace(usageLine, `"input_tokens":100`, `"input_tokens":900`, 1))
			if replace {
				temp := filepath.Join(d, "replacement")
				os.WriteFile(temp, fresh, 0600)
				if err := os.Rename(temp, p); err != nil {
					t.Fatal(err)
				}
			} else {
				os.WriteFile(p, fresh, 0600)
			}
			if err := r.SaveCheckpoint(c); err == nil {
				t.Fatal("stale state was paired with a new source signature")
			}
			after, _ := os.ReadFile(c)
			if string(after) != string(good) {
				t.Fatal("failed save destroyed valid checkpoint")
			}
			resumed := New(p)
			resumed.RestoreCheckpoint(c)
			if _, err := resumed.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if resumed.State.Snapshot().Total != 920 {
				t.Fatalf("stale cache survived recovery: %d", resumed.State.Snapshot().Total)
			}
		})
	}
}

func TestSameLengthRewriteAndEmptyTruncationPublishReset(t *testing.T) {
	for _, middle := range []bool{false, true} {
		t.Run(map[bool]string{false: "small", true: "outside_windows"}[middle], func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "source")
			body := header + usageLine
			if middle {
				body = header + strings.Repeat(" ", 512) + "\n" + usageLine + strings.Repeat(" ", 512) + "\n"
			}
			os.WriteFile(p, []byte(body), 0600)
			r := New(p)
			r.Update(context.Background())
			changed := strings.Replace(body, `"input_tokens":100`, `"input_tokens":900`, 1)
			os.WriteFile(p, []byte(changed), 0600)
			stamp := r.info.ModTime().Add(time.Second)
			os.Chtimes(p, stamp, stamp)
			if updated, err := r.Update(context.Background()); err != nil || !updated || r.State.Snapshot().Total != 920 {
				t.Fatal("same-sized rewrite retained stale state")
			}
			os.WriteFile(p, nil, 0600)
			if updated, err := r.Update(context.Background()); err != nil || !updated || r.State.HasUsage {
				t.Fatal("empty truncation was not published as a reset")
			}
		})
	}
}

func TestAppendBeforeCheckpointAndPartialResumeRemainIncremental(t *testing.T) {
	d := t.TempDir()
	p, c := filepath.Join(d, "source"), filepath.Join(d, "cache")
	os.WriteFile(p, []byte(header+usageLine+`{"type":"response_item","payload":"`), 0600)
	r := New(p)
	r.Update(context.Background())
	appendFile(t, p, "ignored\"}\n")
	if err := r.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	resumed := New(p)
	if !resumed.RestoreCheckpoint(c) {
		t.Fatal("append invalidated valid prefix")
	}
	resumed.Update(context.Background())
	full := New(p)
	full.Update(context.Background())
	if resumed.State.Lines != full.State.Lines || !resumed.State.Snapshot().Equal(full.State.Snapshot()) {
		t.Fatal("partial checkpoint lost or duplicated records")
	}
}

func TestSegmentDigestsMatchSourceAcrossChunkBoundaries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source")
	data := []byte(strings.Repeat("0123456789abcdef", 300))
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, chunk := range []int{1, 17, 256, 257, 65536} {
		actual := &Reader{proofs: []prefixProof{{Limit: 123, hasher: sha256.New()}, {Limit: -1, hasher: sha256.New()}}}
		for at := 0; at < len(data); {
			end := min(len(data), at+chunk)
			actual.feedProofs(data[at:end])
			at = end
			first := min(at, 123)
			if actual.proofs[0].sum() != sha256.Sum256(data[:first]) || actual.proofs[1].sum() != sha256.Sum256(data[first:at]) {
				t.Fatalf("consumed fingerprint differs at chunk=%d offset=%d", chunk, at)
			}
		}
	}
}

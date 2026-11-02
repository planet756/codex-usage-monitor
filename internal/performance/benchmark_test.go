// Package performance provides deterministic synthetic workloads. These are
// Linux Go costs, not measurements of the native Windows companion processes.
package performance

import (
	"bytes"
	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/presentation"
	"codex-usage-monitor/internal/rollout"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var outFrame *ipc.Frame
var outInt int64
var outBool bool
var outModel presentation.Model

const chat = "00000000-0000-7000-8000-000000000001"

func sample() metrics.Snapshot {
	s := metrics.New()
	s.ThreadID = chat
	s.HasUsage = true
	s.HasCache = true
	s.Usage = metrics.Usage{Input: 100000, Cached: 70000, Output: 5000}
	s.DecodeTokens = 5000
	s.DecodeMS = 25000
	s.DecodeSamples = 20
	s.TurnCount = 10
	s.StepCount = 20
	return s.Snapshot()
}
func BenchmarkSnapshotEquality(b *testing.B) {
	a, c := sample(), sample()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		outBool = a.Equal(c)
	}
}
func BenchmarkDisplayModel(b *testing.B) {
	s := sample()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		outModel = presentation.Display(s, "en", chat, "", false, false, false)
	}
}
func TestDiagnosticUpdatesDoNotInvalidateDisplay(t *testing.T) {
	s := sample()
	var stream live.View
	first := presentation.Display(s, "en", chat, "", false, false, false)
	for range 100 {
		stream.Frames++
		stream.Revision++
		if presentation.Display(s, "en", chat, "", false, false, false) != first {
			t.Fatal("diagnostic-only update invalidated display")
		}
	}
}
func frame(payload int) []byte {
	return []byte(`{"type":"broadcast","method":"thread-stream-state-changed","sourceClientId":"desktop","version":11,"params":{"conversationId":"` + chat + `","hostId":"local","change":{"type":"snapshot","revision":1,"conversationState":{"latestTokenUsageInfo":{"total":{"inputTokens":1000,"cachedInputTokens":500,"outputTokens":100}},"ignoredText":"` + strings.Repeat("x", payload) + `"}}}}`)
}
func BenchmarkIPCDispatch(b *testing.B) {
	for _, n := range []int{1024, 512 << 10, 4 << 20} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			packet := frame(n)
			session := ipc.Session{Self: "monitor"}
			session.Router.SetSelf("monitor")
			var stream live.Stream
			stream.Reset(chat, "local", "monitor")
			b.ReportAllocs()
			b.SetBytes(int64(len(packet)))
			b.ResetTimer()
			for range b.N {
				f, e := ipc.Decode(packet)
				if e != nil {
					b.Fatal(e)
				}
				session.Handle(f)
				stream.Apply(f)
			}
		})
	}
}
func BenchmarkIPCReadFrame(b *testing.B) {
	for _, n := range []int{1024, 512 << 10, 4 << 20} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			packet := frame(n)
			var buf bytes.Buffer
			var h [4]byte
			binary.LittleEndian.PutUint32(h[:], uint32(len(packet)))
			buf.Write(h[:])
			buf.Write(packet)
			raw := buf.Bytes()
			var reader ipc.FrameReader
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for range b.N {
				v, e := reader.Read(bytes.NewReader(raw))
				if e != nil {
					b.Fatal(e)
				}
				outFrame = v
			}
		})
	}
}

func BenchmarkIPCLargePatch(b *testing.B) {
	packet := []byte(`{"params":{"change":{"patches":[{"op":"replace","path":["turns",0,"items",0],"value":{"ignoredText":"` + strings.Repeat("x", 4<<20) + `"}}]}}}`)
	b.SetBytes(int64(len(packet)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		f, err := ipc.Decode(packet)
		if err != nil {
			b.Fatal(err)
		}
		outFrame = f
	}
}
func BenchmarkObserve(b *testing.B) {
	for _, kind := range []string{"accounting", "irrelevant-2MiB", "event-2MiB"} {
		b.Run(kind, func(b *testing.B) {
			line := []byte(`{"type":"token_usage_record","timestamp":"2026-10-22T00:00:01Z","payload":{"turn_id":"turn","response_id":"response","thread_token_usage":{"input_tokens":1000,"cached_input_tokens":500,"output_tokens":100},"usage":{"input_tokens":1000,"output_tokens":100}}}`)
			if kind == "irrelevant-2MiB" {
				line = []byte(`{"type":"response_item","payload":{"type":"message","text":"` + strings.Repeat("x", 2<<20) + `"}}`)
			}
			if kind == "event-2MiB" {
				line = []byte(`{"type":"event_msg","payload":{"type":"agent_message","message":"` + strings.Repeat("x", 2<<20) + `"}}`)
			}
			s := metrics.New()
			keys := make(map[string]bool)
			b.ReportAllocs()
			b.SetBytes(int64(len(line)))
			b.ResetTimer()
			for range b.N {
				s.ObserveWith(line, func(r *metrics.Record) (bool, error) {
					return s.Admit(r, func(key string) (bool, error) { old := keys[key]; keys[key] = true; return old, nil })
				})
			}
			outInt = s.Lines
		})
	}
}
func fixture(b *testing.B) string {
	b.Helper()
	var data bytes.Buffer
	for i := 0; i < 10000; i++ {
		turn := fmt.Sprintf("turn-%d", i/10)
		start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC).UnixMilli() + int64(i)*2000
		if i%10 == 0 {
			fmt.Fprintf(&data, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"%s\"}}\n", turn)
		}
		fmt.Fprintf(&data, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"item_completed\",\"turn_id\":\"%s\",\"item\":{\"type\":\"AgentMessage\"},\"started_at_ms\":%d,\"completed_at_ms\":%d}}\n", turn, start, start+1000)
		fmt.Fprintf(&data, "{\"type\":\"token_usage_record\",\"timestamp\":\"%s\",\"payload\":{\"turn_id\":\"%s\",\"response_id\":\"response-%d\",\"thread_token_usage\":{\"input_tokens\":%d,\"cached_input_tokens\":%d,\"output_tokens\":%d},\"usage\":{\"input_tokens\":100,\"output_tokens\":10}}}\n", time.UnixMilli(start+1000).UTC().Format(time.RFC3339Nano), turn, i, (i+1)*100, (i+1)*50, (i+1)*10)
	}
	p := filepath.Join(b.TempDir(), "synthetic.jsonl")
	if e := os.WriteFile(p, data.Bytes(), 0600); e != nil {
		b.Fatal(e)
	}
	return p
}
func BenchmarkReplay(b *testing.B) {
	p := fixture(b)
	info, _ := os.Stat(p)
	b.SetBytes(info.Size())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := rollout.New(p)
		if _, e := r.Update(context.Background()); e != nil {
			r.Close()
			b.Fatal(e)
		}
		outInt = r.State.StepCount
		r.Close()
		if outInt != 10000 {
			b.Fatalf("unexpected steps %d", outInt)
		}
	}
}

package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func sourceSnapshot(body string) string {
	return `{"type":"broadcast","method":"thread-stream-state-changed","sourceClientId":"owner","version":11,"targetClientIds":["monitor"],"params":{"conversationId":"01a1695c-c599-7d43-8e01-b207f5a74431","hostId":"local","change":{"type":"snapshot","revision":31,"conversationState":{` + body + `}}}}`
}

func TestLargeSnapshotKeepsOnlyVerifiedSourceMetadataAndNextFrame(t *testing.T) {
	private := strings.Repeat(`private [brackets] {nested} \\ \" `, (MaxFrame/32)+1)
	body := sourceSnapshot(`"turns":[{"text":"` + private + `","ignored":{"nested":[true,false,null,-123.4e+5]}}],"rolloutPath":"C:\\logs\\selected.jsonl"`)
	if !json.Valid([]byte(body)) || len(body) <= MaxFrame {
		t.Fatal("invalid large fixture")
	}
	var wire bytes.Buffer
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
	wire.Write(header[:])
	wire.WriteString(body)
	if err := WriteFrame(&wire, map[string]string{"method": "next"}); err != nil {
		t.Fatal(err)
	}
	var r FrameReader
	f, err := r.Read(&wire)
	if err != nil || !f.SourceOnly || f.Params.Change.State.RolloutPath != `C:\logs\selected.jsonl` || f.Params.Change.Revision != 31 || f.Source != "owner" || len(f.Targets) != 1 {
		t.Fatalf("source projection failed: %+v %v", f, err)
	}
	if cap(r.scratch) > 256<<10 {
		t.Fatal("large body retained")
	}
	if f, err = r.Read(&wire); err != nil || f.Method != "next" {
		t.Fatal("source projection lost next frame", err)
	}
}

func TestStreamingProjectionValidatesIgnoredJSONAndDrainsFailures(t *testing.T) {
	for _, body := range []string{
		`"rolloutPath":"selected.jsonl","ignored":"bad\x"`,
		`"rolloutPath":"selected.jsonl","ignored":[1,]`,
		`"rolloutPath":"selected.jsonl","ignored":{"a":01}`,
		`"rolloutPath":"selected.jsonl","ignored":tru`,
		`"rolloutPath":"selected.jsonl","ignored":1e`,
		`"rolloutPath":"selected.jsonl","ignored":"bad` + "\n" + `"`,
	} {
		data := sourceSnapshot(body)
		r := strings.NewReader(data + "NEXT")
		if _, err := projectLargeSource(r, len(data)); !errors.Is(err, ErrFrameOversized) {
			t.Fatal("malformed JSON accepted", err)
		}
		rest, _ := io.ReadAll(r)
		if string(rest) != "NEXT" {
			t.Fatal("invalid source projection consumed next frame")
		}
	}
	if _, err := projectLargeSource(strings.NewReader("{}"), 100); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("truncated wire accepted", err)
	}
	for _, body := range []string{
		`"rolloutPath":"old.jsonl","rolloutPath":"new.jsonl"`,
		`"rolloutPath":"old.jsonl","RolloutPath":"new.jsonl"`,
		`"rollout\u0050ath":"new.jsonl","ignored":{"deep":[{"k":"x"}]}`,
	} {
		data := sourceSnapshot(body)
		f, err := projectLargeSource(strings.NewReader(data), len(data))
		if err != nil || f.Params.Change.State.RolloutPath != "new.jsonl" {
			t.Fatal("JSON key/duplicate semantics changed", err)
		}
	}
}

func TestLargePatchKeepsRoutingForSourceRecoveryAndNextFrame(t *testing.T) {
	body := `{"type":"broadcast","method":"thread-stream-state-changed","sourceClientId":"owner","version":11,"params":{"conversationId":"` + id1 + `","hostId":"local","change":{"type":"patches","baseRevision":30,"revision":31,"patches":[{"op":"replace","path":["turns",0,"text"],"value":"` + strings.Repeat("x", MaxFrame) + `"},{"op":"replace","path":["rolloutPath"],"value":"new.jsonl"}]}}}`
	var wire bytes.Buffer
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(body)))
	wire.Write(prefix[:])
	wire.WriteString(body)
	WriteFrame(&wire, map[string]string{"method": "next"})
	var reader FrameReader
	f, err := reader.Read(&wire)
	if err != nil || !f.SourceOnly || f.Params.Change.Type != "patches" || f.Params.Change.Base != 30 || f.Params.Change.Revision != 31 || len(f.Params.Change.Patches) != 0 {
		t.Fatal("large delta lost recovery metadata", err)
	}
	if f, err = reader.Read(&wire); err != nil || f.Method != "next" {
		t.Fatal("large delta lost following frame", err)
	}
}

func FuzzStreamingSourceProjection(f *testing.F) {
	f.Add([]byte(sourceSnapshot(`"rolloutPath":"C:\\logs\\selected.jsonl","ignored":[{"a":"\\\""},false,null,-1.2e-4]`)))
	f.Add([]byte(sourceSnapshot(`"rolloutPath":"selected.jsonl"`)))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 256<<10 {
			return
		}
		got, err := projectLargeSource(bytes.NewReader(b), len(b))
		if err != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatal("invalid JSON produced branch authority")
		}
		want, err := Decode(b)
		if err != nil {
			t.Fatal("projection accepted invalid metadata", err)
		}
		if got.Type != want.Type || got.Method != want.Method || got.Source != want.Source || got.Version != want.Version || got.Params.ID != want.Params.ID || got.Params.Host != want.Params.Host || got.Params.Change.Revision != want.Params.Change.Revision || got.Params.Change.State.RolloutPath != want.Params.Change.State.RolloutPath {
			t.Fatal("projected source differs from standard decode")
		}
	})
}

func BenchmarkLargeSourceProjection(b *testing.B) {
	data := []byte(sourceSnapshot(`"ignored":"` + strings.Repeat("x", MaxFrame) + `","rolloutPath":"selected.jsonl"`))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := projectLargeSource(bytes.NewReader(data), len(data)); err != nil {
			b.Fatal(err)
		}
	}
}

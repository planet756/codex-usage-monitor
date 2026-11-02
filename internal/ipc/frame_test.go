package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestProjectionBudgetsRejectOversizedArraysAndKeepNextFrame(t *testing.T) {
	bad := []byte(`{"params":{"change":{"patches":[` + strings.Repeat(`{"path":[],"op":"replace"},`, 4096) + `{"path":[],"op":"replace"}]}}}`)
	var wire bytes.Buffer
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(bad)))
	wire.Write(h[:])
	wire.Write(bad)
	WriteFrame(&wire, map[string]string{"method": "next"})
	var reader FrameReader
	if _, err := reader.Read(&wire); !errors.Is(err, ErrFrameSkipped) {
		t.Fatalf("projection budget not enforced: %v", err)
	}
	f, err := reader.Read(&wire)
	if err != nil || f.Method != "next" {
		t.Fatal("skipped projection lost frame alignment")
	}
}

func TestLargeFrameScratchIsNotRetained(t *testing.T) {
	var wire bytes.Buffer
	WriteFrame(&wire, map[string]string{"ignored": strings.Repeat("x", 512<<10)})
	var reader FrameReader
	if _, err := reader.Read(&wire); err != nil {
		t.Fatal(err)
	}
	if cap(reader.scratch) > 256<<10 {
		t.Fatal("history payload remained in the idle reader")
	}
}

func TestProjectionArrayEscapesNestedValuesAndLimits(t *testing.T) {
	good := []byte(`{"targetClientIds":["a,]\\\"b"],"params":{"change":{"patches":[{"op":"replace","path":["turns",0,"a,]\\\"b"],"value":{"ignored":[{"text":"[,]\\\""}]}},{"op":"replace","path":["status"],"value":"completed"}]}}}`)
	f, err := Decode(good)
	if err != nil || len(f.Params.Change.Patches) != 2 || len(f.Targets) != 1 || f.Params.Change.Patches[1].Path[0] != "status" {
		t.Fatalf("escaped/nested projection failed: %v", err)
	}
	for _, bad := range []string{
		`{"targetClientIds":[` + strings.Repeat(`"a",`, 256) + `"last"]}`,
		`{"params":{"change":{"patches":[{"path":[` + strings.Repeat(`0,`, 32) + `0]}]}}}`,
		`{"method":"` + strings.Repeat("x", 129) + `"}`,
	} {
		if _, err := Decode([]byte(bad)); !errors.Is(err, ErrFrameSkipped) {
			t.Fatalf("metadata limit not enforced: %v", err)
		}
	}
}

func TestSourceSchemaRejectsUnknownChangesAndOperationsAtDecodeBoundary(t *testing.T) {
	for _, change := range []string{
		`{"type":"unknown","revision":1}`,
		`{"type":"snapshot","revision":-1}`,
		`{"type":"patches","baseRevision":2,"revision":1}`,
		`{"type":"patches","baseRevision":1,"revision":2,"patches":[{"op":"move","path":["rolloutPath"]}]}`,
	} {
		body := `{"type":"broadcast","method":"thread-stream-state-changed","version":11,"params":{"change":` + change + `}}`
		if _, err := Decode([]byte(body)); !errors.Is(err, ErrFrameSkipped) {
			t.Fatal("unvalidated source change entered the stream", err)
		}
	}
}

package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

const id1 = "01a159db-a637-7512-8d64-06de0e0e785b"
const id2 = "01a15da4-16e9-7951-8fce-50f7bef8bb83"

type tinyReader struct{ io.Reader }

func (r tinyReader) Read(b []byte) (int, error) { return r.Reader.Read(b[:min(1, len(b))]) }

func TestFragmentedFramesAndLengthLimit(t *testing.T) {
	var b bytes.Buffer
	if e := WriteFrame(&b, map[string]string{"method": "initialize"}); e != nil {
		t.Fatal(e)
	}
	p, e := new(FrameReader).Read(tinyReader{&b})
	if e != nil || p.Method != "initialize" {
		t.Fatalf("fragmented frame: %+v %v", p, e)
	}
	if _, e = new(FrameReader).Read(bytes.NewReader([]byte{255, 255, 255, 127})); e == nil {
		t.Fatal("accepted unbounded allocation")
	}
}

func send(r *Router, source, host, id string, following bool) {
	p, _ := json.Marshal(map[string]any{"type": "broadcast", "method": "thread-stream-following-changed", "sourceClientId": source, "params": map[string]any{"conversationId": id, "hostId": host, "following": following}})
	r.Observe(decodeTest(p))
}

func TestRouterSwitchesAndRefusesAmbiguousWindows(t *testing.T) {
	var r Router
	send(&r, "window1", "local", id1, true)
	if id, _ := r.Selected(); id != id1 {
		t.Fatal("did not bind active thread")
	}
	send(&r, "window1", "local", id2, true)
	send(&r, "window1", "local", id1, false)
	if id, _ := r.Selected(); id != id2 {
		t.Fatal("stale unfollow erased the new selection")
	}
	send(&r, "window2", "local", id1, true)
	if id, n := r.Selected(); id != "" || n != 2 {
		t.Fatal("mixed two window routes")
	}
	r.Observe(decodeTest([]byte(`{"type":"broadcast","method":"client-status-changed","params":{"clientId":"window2","status":"disconnected"}}`)))
	if id, n := r.Selected(); id != id2 || n != 1 {
		t.Fatal("disconnected window was not removed")
	}
	send(&r, "window3", "local", "../../secrets", true)
	if _, n := r.Selected(); n != 1 {
		t.Fatal("accepted an invalid ID")
	}
}

func decodeTest(b []byte) *Frame {
	f, err := Decode(b)
	if err != nil {
		panic(err)
	}
	return f
}

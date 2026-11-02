package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func TestSessionBootstrapsCurrentChatWithoutFocusAndUnfollowsOnSwitch(t *testing.T) {
	var s Session
	out, changed := s.Handle(decodeTest([]byte(`{"type":"response","method":"initialize","resultType":"success","result":{"clientId":"monitor"}}`)))
	if changed || len(out) != 1 || s.Self != "monitor" {
		t.Fatal("initialization did not announce a connected read-only monitor")
	}
	follow := func(id string) []byte {
		b, _ := json.Marshal(map[string]any{"type": "broadcast", "sourceClientId": "desktop-window", "method": "thread-stream-following-changed", "version": 1, "params": map[string]any{"hostId": "local", "conversationId": id, "following": true}})
		return b
	}
	out, changed = s.SetView(id1, "local")
	if !changed || s.Active.ID != id1 || len(out) != 1 {
		t.Fatal("did not subscribe to the already open chat")
	}
	if more, changed := s.Handle(decodeTest(follow(id1))); changed || len(more) != 0 {
		t.Fatal("late membership restarted a verified chat")
	}
	echo, _ := json.Marshal(out[0])
	if _, changed = s.Handle(decodeTest(echo)); changed || s.Active.ID != id1 {
		t.Fatal("monitor subscription was mistaken for a Desktop window")
	}
	out, changed = s.Handle(decodeTest(follow(id2)))
	if changed || s.Active.ID != id1 || len(out) != 0 {
		t.Fatal("a background retained chat replaced the selected page")
	}
	out, changed = s.SetView(id2, "local")
	if !changed || s.Active.ID != id2 || len(out) != 2 {
		t.Fatal("chat switch did not unsubscribe the old chat before subscribing")
	}
	for i, message := range out {
		m := message.(map[string]any)
		p := m["params"].(map[string]any)
		if m["version"] != 1 || p["following"] != (i == 1) || p["conversationId"] != []string{id1, id2}[i] {
			t.Fatalf("unexpected switch messages: %#v", out)
		}
	}
}

func TestSessionNeverUsesFollowingOrderAsCurrentView(t *testing.T) {
	var s Session
	s.Handle(decodeTest([]byte(`{"type":"response","method":"initialize","resultType":"success","result":{"clientId":"monitor"}}`)))
	send(&s.Router, "desktop", "local", id1, true)
	send(&s.Router, "desktop", "local", id2, true)
	if s.Active.ID != "" {
		t.Fatal("watched chats became a selected view")
	}
	if out, changed := s.SetView(id1, ""); !changed || len(out) != 1 || s.Active.ID != id1 {
		t.Fatal("could not select the first watched chat after a later broadcast")
	}
	if out, changed := s.SetView("", ""); !changed || len(out) != 1 || s.Active.ID != "" {
		t.Fatal("unresolved page kept another chat's subscription")
	}
}

func TestSessionRefusesUnrelatedTargetsAndCLISelections(t *testing.T) {
	var s Session
	s.Handle(decodeTest([]byte(`{"type":"response","method":"initialize","resultType":"success","result":{"clientId":"monitor"}}`)))
	b := []byte(`{"type":"broadcast","sourceClientId":"other","targetClientIds":["another-monitor"],"method":"thread-stream-following-changed","params":{"hostId":"local","conversationId":"` + id1 + `","following":true}}`)
	if _, changed := s.Handle(decodeTest(b)); changed || s.Active.ID != "" {
		t.Fatal("accepted another client's targeted current-chat state")
	}
	s.Handle(decodeTest([]byte(`{"type":"broadcast","sourceClientId":"router","method":"client-status-changed","params":{"clientId":"cli","clientType":"cli","status":"connected"}}`)))
	send(&s.Router, "cli", "local", id1, true)
	if id, _ := s.Router.Selected(); id != "" {
		t.Fatal("a CLI process became the current Desktop chat")
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestLargeHistoryFrameIsDrainedWithoutLosingNextPatch(t *testing.T) {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], MaxFrame+1)
	var next bytes.Buffer
	if e := WriteFrame(&next, map[string]string{"method": "next-patch"}); e != nil {
		t.Fatal(e)
	}
	r := io.MultiReader(bytes.NewReader(header[:]), io.LimitReader(zeroReader{}, MaxFrame+1), &next)
	if _, e := new(FrameReader).Read(r); !errors.Is(e, ErrFrameSkipped) {
		t.Fatalf("large history was not drained: %v", e)
	}
	b, e := new(FrameReader).Read(r)
	if e != nil || b.Method != "next-patch" {
		t.Fatal("history drain corrupted the following frame")
	}
}

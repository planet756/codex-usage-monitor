package ipc

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRolloutPathProjectionDoesNotRetainOtherScalarText(t *testing.T) {
	f, err := Decode([]byte(`{"params":{"change":{"conversationState":{"rolloutPath":"C:\\logs\\selected.jsonl"},"patches":[{"value":"PRIVATE_MESSAGE_BODY","path":["turns",0,"text"],"op":"replace"},{"value":"C:\\logs\\replacement.jsonl","op":"replace","path":["rolloutPath"]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Params.Change.State.RolloutPath != `C:\logs\selected.jsonl` || f.Params.Change.Patches[1].Value.Path != `C:\logs\replacement.jsonl` {
		t.Fatal("explicit source path was lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", f.Params.Change.Patches), "PRIVATE_MESSAGE_BODY") {
		t.Fatal("ignored text retained")
	}
}

func TestRolloutPathLimitsAndNull(t *testing.T) {
	for _, value := range []string{`true`, `17`, `{}`, `"` + strings.Repeat("x", maxSourcePathBytes+1) + `"`} {
		for _, body := range []string{
			`{"params":{"change":{"conversationState":{"rolloutPath":` + value + `}}}}`,
			`{"params":{"change":{"patches":[{"op":"replace","path":["rolloutPath"],"value":` + value + `}]}}}`,
		} {
			if _, err := Decode([]byte(body)); !errors.Is(err, ErrFrameSkipped) {
				t.Fatalf("invalid source accepted: %v", err)
			}
		}
	}
	for _, value := range []string{`null`, `""`} {
		f, err := Decode([]byte(`{"params":{"change":{"patches":[{"op":"replace","path":["rolloutPath"],"value":` + value + `}]}}}`))
		if err != nil || f.Params.Change.Patches[0].Value.Path != "" {
			t.Fatal("cleared path not represented")
		}
	}
}

func TestSourcePathsParticipateInAggregateProjectionBudget(t *testing.T) {
	patch := `{"op":"replace","path":["rolloutPath"],"value":"` + strings.Repeat("p", maxSourcePathBytes) + `"}`
	_, err := Decode([]byte(`{"params":{"change":{"patches":[` + strings.Repeat(patch+",", 64) + patch + `]}}}`))
	if !errors.Is(err, ErrFrameSkipped) {
		t.Fatal("path strings escaped aggregate memory bound", err)
	}
}

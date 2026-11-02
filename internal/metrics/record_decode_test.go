package metrics

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestProjectedRecordsMatchStandardDecoder(t *testing.T) {
	for _, body := range []string{
		`{"type":"response_item","ordinal":4,"payload":{"text":%s}}`,
		`{"type":"event_msg","ordinal":4,"payload":{"type":"item_completed","turn_id":"a","started_at_ms":10,"completed_at_ms":20,"item":{"type":"Reasoning","text":%s}}}`,
		`{"type":"token_usage_record","ordinal":4,"payload":{"turn_id":"a","response_id":"b","usage":{"input_tokens":100,"output_tokens":10,"unknown":%s},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":10}}}`,
		`{"type":"token_usage_record","ordinal":4,"payload":{"turn_id":"a","response_id":"b","usage":{"input_tokens":null,"output_tokens":10,"unknown":%s}}}`,
		`{"\u0054ype":"event_msg","payload":{"TYPE":"task_complete","turn_id":"a","time_to_first_token_ms":10,"unknown":%s},"ordinal":4,"ordinal":5}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"a","item":null,"unknown":%s}}`,
	} {
		for _, value := range []any{strings.Repeat(`escape \\" ☃️ `, 1000), []any{strings.Repeat("x", 10000), map[string]any{"a": []any{1, nil, true, "{}"}}}} {
			text, _ := json.Marshal(value)
			line := []byte(fmt.Sprintf(body, text))
			var expected Record
			wantErr := json.Unmarshal(line, &expected)
			got, err := DecodeRecord(line)
			if (err == nil) != (wantErr == nil) || !reflect.DeepEqual(got, expected) {
				t.Fatalf("projection changed parsed accounting: err=%v want=%v", err, wantErr)
			}
		}
	}
}

func FuzzProjectedRecord(f *testing.F) {
	f.Add([]byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"a"}}`))
	f.Add([]byte(`{"type":"token_usage_record","ordinal":0,"payload":{"turn_id":"a","usage":{"output_tokens":1}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		line := append(append([]byte{}, b...), []byte(strings.Repeat(" ", 4096))...)
		var expected Record
		wantErr := json.Unmarshal(line, &expected)
		got, err := DecodeRecord(line)
		if (err == nil) != (wantErr == nil) {
			t.Fatal("projection changed validity")
		}
		if err == nil && !reflect.DeepEqual(got, expected) {
			t.Fatal("projection changed accounting fields")
		}
	})
}

func TestFullyEscapedLongAccountingKey(t *testing.T) {
	var key strings.Builder
	for _, r := range "reasoning_output_tokens" {
		fmt.Fprintf(&key, `\u%04x`, r)
	}
	line := []byte(`{"type":"token_usage_record","payload":{"usage":{"input_tokens":100,"output_tokens":20,"` + key.String() + `":10},"ignored":"` + strings.Repeat("x", 5000) + `"}}`)
	r, err := DecodeRecord(line)
	if err != nil || r.Payload.Usage == nil || r.Payload.Usage.Reasoning != 10 {
		t.Fatalf("escaped accounting key lost: %v", err)
	}
}

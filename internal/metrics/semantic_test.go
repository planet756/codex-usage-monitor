package metrics

import (
	"strings"
	"testing"
)

func TestSemanticHashRetainsAccountingAndMissingFields(t *testing.T) {
	base := `{"ordinal":1,"type":"token_usage_record","timestamp":"1970-01-01T00:00:02Z","payload":{"turn_id":"a","response_id":"r","usage":{"input_tokens":100,"output_tokens":20,"reasoning_output_tokens":10}}}`
	r, err := DecodeRecord([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	want := r.SemanticHash()
	for _, changed := range []string{
		strings.Replace(base, `"ordinal":1`, `"ordinal":2`, 1),
		strings.Replace(base, `"output_tokens":20`, `"output_tokens":21`, 1),
		strings.Replace(base, `"reasoning_output_tokens":10`, `"reasoning_output_tokens":11`, 1),
		strings.Replace(base, `"input_tokens":100`, `"input_tokens":100,"cached_input_tokens":0`, 1),
		strings.Replace(base, `"input_tokens":100,`, "", 1),
		strings.Replace(base, `"turn_id":"a"`, `"turn_id":"b"`, 1),
		strings.Replace(base, `"response_id":"r"`, `"response_id":"s"`, 1),
		strings.Replace(base, "00:00:02Z", "00:00:03Z", 1),
	} {
		x, e := DecodeRecord([]byte(changed))
		if e != nil || x.SemanticHash() == want {
			t.Fatal("accounting or absence change lost from semantic digest")
		}
	}
	for _, same := range []string{strings.Replace(base, "00:00:02Z", "00:00:02.000000000+00:00", 1), strings.Replace(base, `"turn_id":"a"`, `"turn_id":"a","text":"irrelevant body"`, 1)} {
		x, e := DecodeRecord([]byte(same))
		if e != nil || x.SemanticHash() != want {
			t.Fatal("formatting or unrelated body changed semantic digest")
		}
	}
}

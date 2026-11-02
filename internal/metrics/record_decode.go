package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DecodeRecord validates every source byte, then avoids a second byte-by-byte
// traversal of large message/tool bodies by projecting only accounting fields.
// Small records use the standard decoder directly. Projection never trusts a
// substring match or a partially valid JSON document.
func DecodeRecord(line []byte) (Record, error) {
	var r Record
	if len(line) < 4096 || !json.Valid(line) {
		err := json.Unmarshal(line, &r)
		return r, err
	}
	projected, ok := projectRecordObject(bytes.TrimSpace(line), 0)
	if !ok {
		projected = line
	}
	err := json.Unmarshal(projected, &r)
	return r, err
}

const projectionBudget = 64 << 10

func projectRecordObject(b []byte, level int) ([]byte, bool) {
	if len(b) == 0 || b[0] != '{' {
		// Preserve type errors without copying ignored, potentially huge values.
		if level > 0 && len(b) > 0 {
			switch b[0] {
			case '"':
				return []byte(`""`), true
			case '[':
				return []byte(`[]`), true
			}
		}
		return b, len(b) <= projectionBudget
	}
	out := make([]byte, 0, 256)
	out = append(out, '{')
	at := 1
	for {
		at = skipJSONSpace(b, at)
		if b[at] == '}' {
			break
		}
		keyStart := at
		at = skipJSONString(b, at)
		key := b[keyStart:at]
		at = skipJSONSpace(b, at) + 1
		at = skipJSONSpace(b, at)
		start := at
		at = skipJSONValue(b, at)
		value := b[start:at]
		name := ""
		if len(key) <= 256 {
			_ = json.Unmarshal(key, &name)
		}
		child, keep := recordField(name, level)
		if keep {
			if child > 0 {
				var ok bool
				value, ok = projectRecordObject(value, child)
				if !ok {
					return nil, false
				}
			}
			if len(out)+len(key)+len(value)+3 > projectionBudget {
				return nil, false
			}
			if len(out) > 1 {
				out = append(out, ',')
			}
			out = append(out, key...)
			out = append(out, ':')
			out = append(out, value...)
		}
		at = skipJSONSpace(b, at)
		if b[at] == '}' {
			break
		}
		at++
	}
	return append(out, '}'), true
}

func recordField(name string, level int) (int, bool) {
	switch level {
	case 0:
		if strings.EqualFold(name, "payload") {
			return 1, true
		}
		for _, key := range []string{"type", "timestamp", "ordinal"} {
			if strings.EqualFold(name, key) {
				return 0, true
			}
		}
	case 1:
		if strings.EqualFold(name, "item") {
			return 2, true
		}
		if strings.EqualFold(name, "usage") || strings.EqualFold(name, "thread_token_usage") {
			return 3, true
		}
		for _, key := range []string{"type", "id", "turn_id", "thread_id", "history_base", "response_id", "started_at_ms", "completed_at_ms", "time_to_first_token_ms"} {
			if strings.EqualFold(name, key) {
				return 0, true
			}
		}
	case 2:
		return 0, strings.EqualFold(name, "type")
	case 3:
		for _, key := range []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens"} {
			if strings.EqualFold(name, key) {
				return 0, true
			}
		}
	}
	return 0, false
}

func skipJSONSpace(b []byte, at int) int {
	for at < len(b) && (b[at] == ' ' || b[at] == '\n' || b[at] == '\r' || b[at] == '\t') {
		at++
	}
	return at
}

// Called only on json.Valid input. bytes.IndexByte skips long quoted bodies
// with the platform's optimized byte search; escaped quotes remain bounded.
func skipJSONString(b []byte, at int) int {
	at++
	for {
		i := bytes.IndexByte(b[at:], '"')
		end := at + i
		slashes := 0
		for j := end - 1; j >= at && b[j] == '\\'; j-- {
			slashes++
		}
		if slashes%2 == 0 {
			return end + 1
		}
		at = end + 1
	}
}

func skipJSONValue(b []byte, at int) int {
	if b[at] == '"' {
		return skipJSONString(b, at)
	}
	if b[at] != '{' && b[at] != '[' {
		for at < len(b) && b[at] != ',' && b[at] != '}' && b[at] != ']' && b[at] != ' ' && b[at] != '\t' && b[at] != '\n' && b[at] != '\r' {
			at++
		}
		return at
	}
	depth := 1
	at++
	for depth > 0 {
		i := bytes.IndexAny(b[at:], "\"{}[]")
		at += i
		switch b[at] {
		case '"':
			at = skipJSONString(b, at)
			continue
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		at++
	}
	return at
}

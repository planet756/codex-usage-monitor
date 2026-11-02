package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Large snapshots need only these fields to identify the current source. The
// parser validates/skips other JSON without materializing text or tool output.
type sourceFields map[string]sourceFields

var sourceSchema = sourceFields{
	"type": nil, "method": nil, "sourceClientId": nil, "targetClientIds": nil, "version": nil,
	"params": {"conversationId": nil, "hostId": nil,
		"change": {"type": nil, "revision": nil, "baseRevision": nil,
			"conversationState": {"rolloutPath": nil}}},
}

type sourceParser struct {
	r         *bufio.Reader
	capture   []byte
	capturing bool
	kept      int
}

func projectLargeSource(source io.Reader, n int) (*Frame, error) {
	limited := &io.LimitedReader{R: source, N: int64(n)}
	p := sourceParser{r: bufio.NewReaderSize(limited, 32<<10)}
	v, err := p.value(sourceSchema, 0)
	if err == nil {
		err = p.space()
		if err == nil {
			_, err = p.r.Peek(1)
			if errors.Is(err, io.EOF) {
				err = nil
			} else {
				err = ErrFrameOversized
			}
		}
	}
	// A rejected projection still consumes precisely its frame, including bytes
	// already prefetched into the bounded reader, so the next frame stays aligned.
	if _, drainErr := io.Copy(io.Discard, p.r); drainErr != nil {
		return nil, drainErr
	}
	if limited.N != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, ErrFrameOversized
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, ErrFrameOversized
	}
	f, err := Decode(b)
	if err != nil || f.Type != "broadcast" || f.Method != "thread-stream-state-changed" || f.Params.Change.Type != "snapshot" && f.Params.Change.Type != "patches" {
		return nil, ErrFrameOversized
	}
	f.SourceOnly = true
	return f, nil
}

func (p *sourceParser) take() (byte, error) {
	b, err := p.r.ReadByte()
	if err == nil && p.capturing {
		if p.kept+len(p.capture) >= 256<<10 {
			return 0, ErrFrameOversized
		}
		p.capture = append(p.capture, b)
	}
	return b, err
}

func (p *sourceParser) space() error {
	for {
		b, err := p.r.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if b[0] != ' ' && b[0] != '\r' && b[0] != '\n' && b[0] != '\t' {
			return nil
		}
		if _, err := p.take(); err != nil {
			return err
		}
	}
}

func (p *sourceParser) expect(want byte) error {
	b, err := p.take()
	if err != nil {
		return err
	}
	if b != want {
		return ErrFrameOversized
	}
	return nil
}

// quoted validates escapes without allocating skipped string values. Keys are
// bounded separately; only a key needed for schema lookup is decoded.
func (p *sourceParser) quoted(key bool) (string, error) {
	if err := p.expect('"'); err != nil {
		return "", err
	}
	var raw []byte
	if key {
		raw = append(raw, '"')
	}
	for {
		b, err := p.take()
		if err != nil {
			return "", err
		}
		if key {
			if len(raw) >= 1024 {
				return "", ErrFrameOversized
			}
			raw = append(raw, b)
		}
		if b == '"' {
			break
		}
		if b < 0x20 {
			return "", ErrFrameOversized
		}
		if b != '\\' {
			continue
		}
		b, err = p.take()
		if err != nil {
			return "", err
		}
		if key {
			raw = append(raw, b)
		}
		switch b {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			for range 4 {
				b, err = p.take()
				if err != nil {
					return "", err
				}
				if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
					return "", ErrFrameOversized
				}
				if key {
					raw = append(raw, b)
				}
			}
		default:
			return "", ErrFrameOversized
		}
	}
	if !key {
		return "", nil
	}
	var result string
	err := json.Unmarshal(raw, &result)
	return result, err
}

func (p *sourceParser) value(schema sourceFields, depth int) (any, error) {
	if depth > 128 {
		return nil, ErrFrameOversized
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	peek, err := p.r.Peek(1)
	if err != nil {
		return nil, err
	}
	b := peek[0]
	switch b {
	case '{':
		if _, err = p.take(); err != nil {
			return nil, err
		}
		var result map[string]any
		if schema != nil {
			result = make(map[string]any)
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		peek, err = p.r.Peek(1)
		if err != nil {
			return nil, err
		}
		if peek[0] == '}' {
			return result, p.expect('}')
		}
		for {
			key, err := p.quoted(schema != nil)
			if err != nil {
				return nil, err
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			if err = p.expect(':'); err != nil {
				return nil, err
			}
			child, keep := schema[key]
			if !keep && schema != nil {
				for canonical, field := range schema {
					if strings.EqualFold(key, canonical) {
						key, child, keep = canonical, field, true
						break
					}
				}
			}
			var v any
			if keep && child == nil {
				if err = p.space(); err != nil {
					return nil, err
				}
				p.capturing = true
				p.capture = p.capture[:0]
				_, err = p.value(nil, depth+1)
				p.capturing = false
				if err != nil {
					return nil, err
				}
				p.kept += len(p.capture)
				v = json.RawMessage(bytes.Clone(p.capture))
			} else {
				v, err = p.value(child, depth+1)
				if err != nil {
					return nil, err
				}
			}
			if keep {
				result[key] = v
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			sep, err := p.take()
			if err != nil {
				return nil, err
			}
			if sep == '}' {
				break
			}
			if sep != ',' {
				return nil, ErrFrameOversized
			}
			if err = p.space(); err != nil {
				return nil, err
			}
		}
		return result, nil
	case '[':
		if _, err = p.take(); err != nil {
			return nil, err
		}
		if err = p.space(); err != nil {
			return nil, err
		}
		peek, err = p.r.Peek(1)
		if err != nil {
			return nil, err
		}
		if peek[0] == ']' {
			return nil, p.expect(']')
		}
		for {
			if _, err = p.value(nil, depth+1); err != nil {
				return nil, err
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			sep, err := p.take()
			if err != nil {
				return nil, err
			}
			if sep == ']' {
				return nil, nil
			}
			if sep != ',' {
				return nil, ErrFrameOversized
			}
		}
	case '"':
		_, err = p.quoted(false)
		return nil, err
	case 't', 'f', 'n':
		literal := map[byte]string{'t': "true", 'f': "false", 'n': "null"}[b]
		for i := range len(literal) {
			if err = p.expect(literal[i]); err != nil {
				return nil, err
			}
		}
		return nil, nil
	default:
		return nil, p.number()
	}
}

func (p *sourceParser) number() error {
	peek, err := p.r.Peek(1)
	if err != nil {
		return err
	}
	if peek[0] == '-' {
		if _, err = p.take(); err != nil {
			return err
		}
		peek, err = p.r.Peek(1)
		if err != nil {
			return err
		}
	}
	if peek[0] == '0' {
		if _, err = p.take(); err != nil {
			return err
		}
	} else {
		if peek[0] < '1' || peek[0] > '9' {
			return ErrFrameOversized
		}
		if err = p.digits(); err != nil {
			return err
		}
	}
	peek, err = p.r.Peek(1)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if peek[0] == '.' {
		if _, err = p.take(); err != nil {
			return err
		}
		if err = p.requiredDigits(); err != nil {
			return err
		}
	}
	peek, err = p.r.Peek(1)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if peek[0] == 'e' || peek[0] == 'E' {
		if _, err = p.take(); err != nil {
			return err
		}
		peek, err = p.r.Peek(1)
		if err != nil {
			return err
		}
		if peek[0] == '+' || peek[0] == '-' {
			if _, err = p.take(); err != nil {
				return err
			}
		}
		return p.requiredDigits()
	}
	return nil
}
func (p *sourceParser) requiredDigits() error {
	b, err := p.r.Peek(1)
	if err != nil {
		return err
	}
	if b[0] < '0' || b[0] > '9' {
		return ErrFrameOversized
	}
	return p.digits()
}
func (p *sourceParser) digits() error {
	for {
		b, err := p.r.Peek(1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if b[0] < '0' || b[0] > '9' {
			return nil
		}
		if _, err = p.take(); err != nil {
			return err
		}
	}
}

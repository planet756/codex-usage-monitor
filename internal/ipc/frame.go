package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

type Part string

func (p *Part) UnmarshalJSON(b []byte) error {
	if len(b) > 1024 {
		return errors.New("invalid Patch path")
	}
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, (*string)(p))
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil || n < 0 {
		return errors.New("invalid Patch index")
	}
	*p = Part(strconv.FormatInt(n, 10))
	return nil
}

type ObjectValue struct {
	RolloutPath SourcePath `json:"rolloutPath"`
}

const maxSourcePathBytes = 32768

// SourcePath is Desktop's explicit current rollout, never a message-text field.
type SourcePath string

func (p *SourcePath) UnmarshalJSON(b []byte) error {
	*p = ""
	if len(b) > 6*maxSourcePathBytes+2 {
		return ErrFrameSkipped
	}
	if err := json.Unmarshal(b, (*string)(p)); err != nil || len(*p) > maxSourcePathBytes {
		return ErrFrameSkipped
	}
	return nil
}

type ProjectedValue struct {
	Object ObjectValue
	Path   SourcePath
}

type Patch struct {
	Op    string         `json:"op"`
	Path  Path           `json:"path"`
	Value ProjectedValue `json:"value"`
}

// Borrow the raw value only while decoding this patch. Unlike RawMessage this
// does not copy an ignored message body into a second large allocation.
type borrowedValue []byte

func (v *borrowedValue) UnmarshalJSON(b []byte) error { *v = b; return nil }

func (p *Patch) UnmarshalJSON(b []byte) error {
	var wire struct {
		Op    string        `json:"op"`
		Path  Path          `json:"path"`
		Value borrowedValue `json:"value"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*p = Patch{Op: wire.Op, Path: wire.Path}
	if len(wire.Value) == 0 {
		return nil
	}
	if len(p.Path) == 1 && p.Path[0] == "rolloutPath" {
		return json.Unmarshal(wire.Value, &p.Value.Path)
	}
	if len(p.Path) == 0 && p.Op != "remove" && !bytes.Equal(wire.Value, []byte("null")) {
		if len(wire.Value) == 0 || wire.Value[0] != '{' {
			return ErrFrameSkipped
		}
		return json.Unmarshal(wire.Value, &p.Value.Object)
	}
	return nil
}

type Frame struct {
	SourceOnly bool   `json:"-"`
	Type       string `json:"type"`
	RequestID  string `json:"requestId"`
	HandledBy  string `json:"handledByClientId"`
	ResultType string `json:"resultType"`
	Result     struct {
		ClientID string `json:"clientId"`
	} `json:"result"`
	Method  string  `json:"method"`
	Source  string  `json:"sourceClientId"`
	Targets Targets `json:"targetClientIds"`
	Version int     `json:"version"`
	Params  struct {
		ID         string `json:"conversationId"`
		Following  *bool  `json:"following"`
		Client     string `json:"clientId"`
		Status     string `json:"status"`
		ClientType string `json:"clientType"`
		Host       string `json:"hostId"`
		Change     struct {
			Type     string      `json:"type"`
			Revision int64       `json:"revision"`
			Base     int64       `json:"baseRevision"`
			State    ObjectValue `json:"conversationState"`
			Patches  PatchList   `json:"patches"`
		} `json:"change"`
	} `json:"params"`
}

// Decode projects a wire frame once; arbitrary message and tool text is skipped.
func Decode(b []byte) (*Frame, error) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Source) > 256 || len(f.HandledBy) > 256 || len(f.RequestID) > 256 || len(f.Params.Host) > 256 || len(f.Targets) > 256 || len(f.Params.Change.Patches) > 4096 {
		return nil, ErrFrameSkipped
	}
	if len(f.Type) > 64 || len(f.Method) > 128 || len(f.ResultType) > 64 || len(f.Result.ClientID) > 256 ||
		len(f.Params.ID) > 256 || len(f.Params.Client) > 256 || len(f.Params.Status) > 64 || len(f.Params.ClientType) > 64 ||
		len(f.Params.Change.Type) > 64 {
		return nil, ErrFrameSkipped
	}
	for _, p := range f.Params.Change.Patches {
		if len(p.Path) > 32 || len(p.Op) > 16 {
			return nil, ErrFrameSkipped
		}
	}
	if f.Type == "broadcast" && f.Method == "thread-stream-state-changed" && f.Version == 11 {
		c := f.Params.Change
		if c.Type != "snapshot" && c.Type != "patches" || c.Revision < 0 || c.Base < 0 || c.Type == "patches" && c.Revision <= c.Base {
			return nil, ErrFrameSkipped
		}
		for _, p := range c.Patches {
			if p.Op != "add" && p.Op != "replace" && p.Op != "remove" {
				return nil, ErrFrameSkipped
			}
		}
	}
	return &f, nil
}

type PatchList []Patch

// arrayItems walks a validated JSON array without allocating a Decoder buffer
// proportional to a patch's ignored message body. The enclosing json.Unmarshal
// validates syntax before invoking these field projections.
func arrayItems(b []byte, limit int, visit func([]byte) error) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return errors.New("invalid metadata array")
	}
	b = bytes.TrimSpace(b[1 : len(b)-1])
	if len(b) == 0 {
		return nil
	}
	start, depth, count := 0, 0, 0
	quoted := false
	emit := func(end int) error {
		count++
		if count > limit {
			return ErrFrameSkipped
		}
		return visit(bytes.TrimSpace(b[start:end]))
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quoted {
			if c == '\\' {
				i++
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				if err := emit(i); err != nil {
					return err
				}
				start = i + 1
			}
		}
	}
	if quoted || depth != 0 {
		return errors.New("invalid metadata array")
	}
	return emit(len(b))
}

func (p *PatchList) UnmarshalJSON(b []byte) error {
	*p = (*p)[:0]
	used := 0
	return arrayItems(b, 4096, func(raw []byte) error {
		var item Patch
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		used += 192 + len(item.Path)*16 + len(item.Value.Path) + len(item.Value.Object.RolloutPath)
		for _, part := range item.Path {
			used += len(part)
		}
		if used > 2<<20 || len(item.Op) > 16 {
			return ErrFrameSkipped
		}
		*p = append(*p, item)
		return nil
	})
}

type Path []Part

func (p *Path) UnmarshalJSON(b []byte) error {
	*p = (*p)[:0]
	return arrayItems(b, 32, func(raw []byte) error {
		var item Part
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		*p = append(*p, item)
		return nil
	})
}

type Targets []string

func (p *Targets) UnmarshalJSON(b []byte) error {
	*p = (*p)[:0]
	return arrayItems(b, 256, func(raw []byte) error {
		if len(raw) > 1538 {
			return ErrFrameSkipped
		} // 256 UTF-16 escapes plus quotes.
		var item string
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if len(item) > 256 {
			return ErrFrameSkipped
		}
		*p = append(*p, item)
		return nil
	})
}

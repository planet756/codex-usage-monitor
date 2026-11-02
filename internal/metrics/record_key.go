package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// AccountingKey is scoped by stable turn/response identity, not timestamp or
// whole-file hashes. Unattributed records retain Observe's incomplete handling.
func (r Record) AccountingKey(thread string) string {
	p := r.Payload
	if p.TurnID == "" || len(p.TurnID) > 256 || len(p.ResponseID) > 512 || p.ThreadID != "" && thread != "" && !strings.EqualFold(p.ThreadID, thread) {
		return ""
	}
	turn := strconv.Itoa(len(p.TurnID)) + ":" + p.TurnID
	switch r.Type {
	case "token_usage_record":
		if p.ResponseID != "" {
			return "r\x00" + turn + p.ResponseID
		}
	case "event_msg":
		switch p.Type {
		case "task_started", "task_complete", "turn_aborted":
			return p.Type + "\x00" + turn
		case "item_completed":
			switch p.Item.Type {
			case "Reasoning", "AgentMessage", "CommandExecution", "FileChange", "McpToolCall", "WebSearch", "ImageView", "Extension":
				return "i\x00" + turn + p.Item.Type + "\x00" + strconv.FormatInt(p.Start, 10) + "/" + strconv.FormatInt(p.End, 10)
			}
		}
	}
	return ""
}

// Fingerprint contains only fields which affect accounting. Different message
// text, transport formatting or equivalent timestamp spellings are irrelevant.
func (r Record) AccountingFingerprint() string {
	var value any
	if r.Type == "token_usage_record" {
		tm, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		usage := func(u *Usage) any {
			if u == nil {
				return nil
			}
			return struct {
				Value        Usage
				Cache, Valid bool
			}{*u, u.cacheKnown, u.Valid()}
		}
		value = []any{usage(r.Payload.Usage), usage(r.Payload.ThreadUsage), err == nil, tm.UnixMilli()}
	} else if r.Type == "event_msg" && r.Payload.Type == "task_complete" {
		value = r.Payload.TTFT
	} else {
		return "" // The identity key already contains every accounting field.
	}
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r Record) SettledTurnKey() string {
	if r.Type != "token_usage_record" || r.Payload.TurnID == "" || r.Payload.ResponseID == "" {
		return ""
	}
	return "counted-turn\x00" + r.Payload.TurnID
}

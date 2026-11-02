// Package metrics reads only accounting metadata. Message and tool contents are
// never retained in the state or emitted by the inspector.
package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Usage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	cacheKnown bool
	invalid    bool
}

func (u *Usage) UnmarshalJSON(b []byte) error {
	var w struct {
		Input     *int64 `json:"input_tokens"`
		Cached    *int64 `json:"cached_input_tokens"`
		Output    *int64 `json:"output_tokens"`
		Reasoning int64  `json:"reasoning_output_tokens"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*u = Usage{Reasoning: w.Reasoning, cacheKnown: w.Cached != nil, invalid: w.Input == nil || w.Output == nil}
	if w.Input != nil {
		u.Input = *w.Input
	}
	if w.Cached != nil {
		u.Cached = *w.Cached
	}
	if w.Output != nil {
		u.Output = *w.Output
	}
	return nil
}

func (u Usage) Valid() bool {
	return !u.invalid && u.Input >= 0 && u.Output >= 0 && u.Input <= math.MaxInt64-u.Output && u.Cached >= 0 && u.Cached <= u.Input && u.Reasoning >= 0 && u.Reasoning <= u.Output
}

type Interval struct{ Start, End int64 }

// Union returns a sorted, disjoint representation and the newly covered time.
func Union(xs []Interval, x Interval) ([]Interval, int64) {
	if x.Start < 0 || x.End <= x.Start {
		return xs, 0
	}
	// Most records are chronological and disjoint: append without sorting or
	// rescanning all prior intervals. The slow path handles out-of-order overlap.
	if len(xs) == 0 || xs[len(xs)-1].End < x.Start {
		return append(xs, x), x.End - x.Start
	}
	i := sort.Search(len(xs), func(i int) bool { return xs[i].End >= x.Start })
	j := i
	var old int64
	for j < len(xs) && xs[j].Start <= x.End {
		x.Start = min(x.Start, xs[j].Start)
		x.End = max(x.End, xs[j].End)
		old += xs[j].End - xs[j].Start
		j++
	}
	n := len(xs) - (j - i) + 1
	if j == i {
		xs = append(xs, Interval{})
	}
	copy(xs[i+1:], xs[j:])
	xs = xs[:n]
	xs[i] = x
	return xs, x.End - x.Start - old
}

func Length(xs []Interval) (n int64) {
	for _, x := range xs {
		n += x.End - x.Start
	}
	return
}

type Turn struct {
	Started         bool
	Complete        bool
	Incomplete      bool
	Model           []Interval
	Tools           []Interval
	Retired         bool
	PendingDecode   decodeBoundary
	LastResponseMS  int64
	BoundaryUnknown bool
}

// State is checkpointable and bounded to the most recent turns. All-history
// durations are counters; historical text, token arrays and item bodies are absent.
type State struct {
	Usage             Usage
	HasUsage          bool
	UsageStale        bool
	HasCache          bool
	ThreadID          string
	CurrentID         string
	Turns             map[string]*Turn
	Order             []string
	ModelMS           int64
	ToolMS            int64
	HasTiming         bool
	TTFTSum           int64
	TTFTCount         int64
	DecodeTokens      int64
	DecodeMS          int64
	DecodeSamples     int64
	DecodeSkipped     int64
	TurnCount         int64
	StepCount         int64
	CountsIncomplete  bool
	Lines             int64
	Malformed         int64
	Oversized         int64
	MissingRecords    int64
	HistoryComplete   bool
	TimingComplete    bool
	TimingLimited     bool
	retainedIntervals int
}

func New() *State {
	return &State{Turns: make(map[string]*Turn), HistoryComplete: true, TimingComplete: true}
}

func (s *State) turn(id string, previous bool) *Turn {
	if id == "" {
		return nil
	}
	if s.Turns == nil {
		s.Turns = make(map[string]*Turn)
	}
	if t := s.Turns[id]; t != nil {
		return t
	}
	if previous {
		s.TimingComplete = false
		s.TimingLimited = true
		return nil
	}
	t := &Turn{}
	s.Turns[id] = t
	s.Order = append(s.Order, id)
	// Exact membership stays in the reader when timing detail is released.
	if len(s.Order) > 32 {
		old := s.Turns[s.Order[0]]
		s.retainedIntervals -= len(old.Model) + len(old.Tools)
		delete(s.Turns, s.Order[0])
		s.Order = s.Order[1:]
	}
	return t
}

type payload struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	TurnID      string          `json:"turn_id"`
	ThreadID    string          `json:"thread_id"`
	HistoryBase json.RawMessage `json:"history_base"`
	ResponseID  string          `json:"response_id"`
	ThreadUsage *Usage          `json:"thread_token_usage"`
	Usage       *Usage          `json:"usage"`
	Start       int64           `json:"started_at_ms"`
	End         int64           `json:"completed_at_ms"`
	TTFT        *int64          `json:"time_to_first_token_ms"`
	Item        struct {
		Type string `json:"type"`
	} `json:"item"`
}

type Record struct {
	Type             string  `json:"type"`
	Timestamp        string  `json:"timestamp"`
	Ordinal          *int64  `json:"ordinal"`
	Payload          payload `json:"payload"`
	CountResponse    bool    `json:"-"`
	PreviousTurn     bool    `json:"-"`
	FirstSettledTurn bool    `json:"-"`
}

func (r Record) ValidCarrier() bool {
	p := r.Payload
	if r.Type == "" {
		return false
	}
	if r.Type != "event_msg" && r.Type != "token_usage_record" && r.Type != "session_meta" {
		return true
	}
	return !(r.Type == "event_msg" && p.Type == "" || r.Type == "session_meta" && p.ID == "" ||
		len(p.ID) > 256 || len(p.TurnID) > 256 || len(p.ResponseID) > 512)
}

// ObserveWith decodes once, then lets a verified history reader check sequence
// and exact duplicate identity before any accounting state changes.
func (s *State) ObserveWith(line []byte, gate func(*Record) (bool, error)) error {
	s.Lines++
	record, decodeErr := DecodeRecord(line)
	if err := decodeErr; err != nil {
		var typed *json.UnmarshalTypeError
		ignoredPayload := record.Type != "" && record.Type != "event_msg" && record.Type != "token_usage_record" && record.Type != "session_meta" && errors.As(err, &typed) && strings.HasPrefix(typed.Field, "payload")
		if !ignoredPayload {
			s.Malformed++
			s.MarkIncomplete()
			return nil
		}
	}
	if !record.ValidCarrier() {
		s.Malformed++
		s.MarkIncomplete()
		return nil
	}
	accept, err := gate(&record)
	if err != nil {
		s.MarkIncomplete()
		return err
	}
	if !accept {
		return nil
	}
	if record.Type != "event_msg" && record.Type != "token_usage_record" && record.Type != "session_meta" {
		return nil
	}
	s.observeRecord(record)
	return nil
}

func (s *State) observeRecord(record Record) {
	p := record.Payload
	if record.Type == "session_meta" {
		// An explicitly inherited prefix may belong to another logical chat.
		// Its timing/count history is retained, but the new chat reports its own
		// absolute usage baseline, which is allowed to be smaller.
		if s.ThreadID != "" && !strings.EqualFold(s.ThreadID, p.ID) {
			s.Usage, s.HasUsage, s.HasCache = Usage{}, false, false
			s.UsageStale = false
		}
		s.ThreadID = p.ID
		return
	}
	if p.ThreadID != "" && s.ThreadID != "" && !strings.EqualFold(p.ThreadID, s.ThreadID) {
		return
	}
	if record.Type == "token_usage_record" {
		if p.TurnID == "" {
			s.MarkIncomplete()
			s.CountsIncomplete = true
			s.DecodeSkipped++
			if p.ThreadUsage != nil {
				s.applyUsage(*p.ThreadUsage, nil)
			} else {
				s.missingUsage(nil)
			}
			return
		}
		if record.CountResponse {
			s.StepCount++
			if record.FirstSettledTurn {
				s.TurnCount++
			}
		}
		t := s.turn(p.TurnID, record.PreviousTurn)
		if t == nil || t.Retired {
			s.DecodeSkipped++
			s.TimingComplete = false
			if p.ThreadUsage != nil {
				s.applyUsage(*p.ThreadUsage, t)
			} else {
				s.missingUsage(t)
			}
			return
		}
		if p.TurnID != "" && (!t.Complete || s.CurrentID == "") {
			s.CurrentID = p.TurnID
		}
		if p.ThreadUsage != nil {
			s.applyUsage(*p.ThreadUsage, t)
		} else {
			s.missingUsage(t)
		}
		if record.CountResponse {
			ended, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
			s.finishDecode(t, p.Usage, ended)
		} else {
			s.CountsIncomplete = true
			s.TimingComplete = false
			t.PendingDecode = decodeBoundary{}
			ended, err := time.Parse(time.RFC3339Nano, record.Timestamp)
			t.BoundaryUnknown = err != nil
			if err == nil {
				t.LastResponseMS = max(t.LastResponseMS, ended.UnixMilli())
			}
			s.DecodeSkipped++
		}
		return
	}
	// Only explicit identities can attribute lifecycle and timing. Other item
	// carriers (user messages, compaction, etc.) do not contain measured work.
	if p.Type == "item_completed" && !timedItem(p.Item.Type) {
		return
	}
	switch p.Type {
	case "task_started", "item_completed", "task_complete", "turn_aborted":
		if p.TurnID == "" {
			s.MarkIncomplete()
			s.CountsIncomplete = true
			return
		}
	}
	switch p.Type {
	case "task_started":
		t := s.turn(p.TurnID, record.PreviousTurn)
		if t == nil {
			return
		}
		if !t.Started && !t.Complete && !t.Retired {
			t.Started = true
			if p.TurnID != "" {
				s.CurrentID = p.TurnID
			}
		}
	case "item_completed":
		t := s.turn(p.TurnID, record.PreviousTurn)
		if t == nil {
			return
		}
		if t.Retired {
			s.TimingComplete = false
			return
		}
		s.makeRoom(t)
		if p.Start <= 0 || p.End < p.Start || p.End-p.Start > maxDurationMS {
			t.Incomplete = true
			s.TimingComplete = false
			return
		}
		if s.retainedIntervals >= MaxIntervals {
			s.TimingLimited = true
			t.Incomplete = true
			s.TimingComplete = false
			return
		}
		before := len(t.Model) + len(t.Tools)
		var delta int64
		switch p.Item.Type {
		case "Reasoning", "AgentMessage":
			// Late duplicate items from a settled response cannot become the
			// next response's first-output boundary.
			if p.Start >= t.LastResponseMS {
				t.PendingDecode.observe(p.Start, p.End)
			}
			s.HasTiming = true
			t.Model, delta = Union(t.Model, Interval{p.Start, p.End})
			s.addDuration(&s.ModelMS, delta)
		case "CommandExecution", "FileChange", "McpToolCall", "WebSearch", "ImageView", "Extension":
			s.HasTiming = true
			t.Tools, delta = Union(t.Tools, Interval{p.Start, p.End})
			s.addDuration(&s.ToolMS, delta)
		}
		s.retainedIntervals += len(t.Model) + len(t.Tools) - before
		if s.retainedIntervals > MaxIntervals {
			t.Incomplete = true
			s.TimingComplete = false
		}
	case "task_complete":
		t := s.turn(p.TurnID, record.PreviousTurn)
		if t == nil {
			return
		}
		if !t.Complete && p.TTFT != nil {
			n := *p.TTFT
			if n >= 0 && n <= maxDurationMS && s.addDuration(&s.TTFTSum, n) {
				s.TTFTCount++
			} else {
				s.TimingComplete = false
			}
		}
		t.Complete = true
		t.PendingDecode = decodeBoundary{}
	case "turn_aborted":
		t := s.turn(p.TurnID, record.PreviousTurn)
		if t == nil {
			return
		}
		t.Complete = true
		t.PendingDecode = decodeBoundary{}
	}
}

const maxDurationMS = 24 * 60 * 60 * 1000

func timedItem(kind string) bool {
	switch kind {
	case "Reasoning", "AgentMessage", "CommandExecution", "FileChange", "McpToolCall", "WebSearch", "ImageView", "Extension":
		return true
	}
	return false
}

func (s *State) addDuration(total *int64, delta int64) bool {
	if delta < 0 || *total > math.MaxInt64-delta {
		s.TimingComplete = false
		return false
	}
	*total += delta
	return true
}

// A dropped record could contain timing metadata. Exclude new samples from
// affected turns while preserving already settled session decode totals.
func (s *State) MarkIncomplete() {
	s.HistoryComplete = false
	s.TimingComplete = false
	s.CountsIncomplete = true
	for _, t := range s.Turns {
		if !t.Complete {
			t.Incomplete = true
			t.PendingDecode.Invalid = true
		}
	}
}

// The missing records may include responses or timing. Preserve verified
// samples, exclude unfinished turns, and await a newer cumulative usage record.
func (s *State) MarkHistoryGap(missing int64) {
	s.MarkIncomplete()
	s.MissingRecords += missing
	s.UsageStale = true
}

func (s *State) acceptsUsage(t *Turn) bool {
	return !s.HasUsage || t != nil && !t.Retired && (!t.Complete || t == s.Turns[s.CurrentID])
}

func (s *State) missingUsage(t *Turn) {
	if s.acceptsUsage(t) {
		s.UsageStale = true
	}
}

func (s *State) applyUsage(u Usage, t *Turn) {
	if !s.acceptsUsage(t) {
		return
	}
	if !u.Valid() {
		s.UsageStale = true
		s.Malformed++
		if t != nil {
			t.Incomplete = true
		}
		s.HistoryComplete = false
		return
	}
	// Replay order, after lineage validation and exact response deduplication,
	// establishes freshness. A larger counter is not evidence of a newer record.
	// Unattributed/retired records and late records for an older completed turn
	// may seed missing usage, but cannot replace a later identified baseline.
	s.Usage = u
	s.UsageStale = false
	s.HasUsage = true
	s.HasCache = u.cacheKnown
}

type Snapshot struct {
	ThreadID        string   `json:"thread_id"`
	Turns           int64    `json:"turns"`
	Steps           int64    `json:"steps"`
	HasCounts       bool     `json:"has_counts"`
	CountsComplete  bool     `json:"counts_complete"`
	Usage           Usage    `json:"usage"`
	HasUsage        bool     `json:"has_usage"`
	UsageStale      bool     `json:"usage_stale"`
	Total           int64    `json:"total_tokens"`
	Uncached        *int64   `json:"uncached_input_tokens"`
	CachePercent    *float64 `json:"cache_percent"`
	ModelMS         int64    `json:"model_ms"`
	ToolMS          int64    `json:"tool_ms"`
	HasTiming       bool     `json:"has_timing"`
	AverageTTFT     *float64 `json:"average_ttft_ms"`
	TTFTCount       int64    `json:"ttft_samples"`
	TPS             *float64 `json:"tps"`
	TPSApproximate  bool     `json:"tps_approximate"`
	DecodeTokens    int64    `json:"decode_tokens"`
	DecodeMS        int64    `json:"decode_ms"`
	DecodeSamples   int64    `json:"decode_samples"`
	DecodeSkipped   int64    `json:"decode_skipped"`
	Status          string   `json:"status"`
	HistoryComplete bool     `json:"history_complete"`
	TimingComplete  bool     `json:"timing_complete"`
	TimingLimited   bool     `json:"timing_limited"`
	Malformed       int64    `json:"malformed_records"`
	Oversized       int64    `json:"oversized_records"`
	MissingRecords  int64    `json:"missing_records"`
}

// Equal compares numeric values rather than the addresses of optional values.
func (s Snapshot) Equal(other Snapshot) bool {
	equalInt := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	equalFloat := func(a, b *float64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	if !equalInt(s.Uncached, other.Uncached) || !equalFloat(s.CachePercent, other.CachePercent) || !equalFloat(s.AverageTTFT, other.AverageTTFT) || !equalFloat(s.TPS, other.TPS) {
		return false
	}
	s.Uncached, other.Uncached = nil, nil
	s.CachePercent, other.CachePercent = nil, nil
	s.AverageTTFT, other.AverageTTFT = nil, nil
	s.TPS, other.TPS = nil, nil
	// Optional cache presence is represented by Uncached/CachePercent above.
	// The JSON input parser flag is not persisted as part of displayed usage.
	s.Usage.cacheKnown, other.Usage.cacheKnown = false, false
	return s == other
}

func (s Snapshot) WithUsage(u Usage, cacheKnown bool) Snapshot {
	if !u.Valid() {
		return s
	}
	s.Usage, s.HasUsage, s.Total = u, true, u.Input+u.Output
	s.UsageStale = false
	s.Uncached, s.CachePercent = nil, nil
	if cacheKnown {
		n := u.Input - u.Cached
		s.Uncached = &n
		if u.Input > 0 {
			rate := 100 * float64(u.Cached) / float64(u.Input)
			s.CachePercent = &rate
		}
	}
	return s
}

func (s *State) Snapshot() Snapshot {
	x := Snapshot{ThreadID: s.ThreadID, Usage: s.Usage, HasUsage: s.HasUsage && !s.UsageStale, UsageStale: s.UsageStale, Total: s.Usage.Input + s.Usage.Output,
		Turns: s.TurnCount, Steps: s.StepCount, HasCounts: true, CountsComplete: !s.CountsIncomplete,
		ModelMS: s.ModelMS, ToolMS: s.ToolMS, HasTiming: s.HasTiming, TTFTCount: s.TTFTCount,
		HistoryComplete: s.HistoryComplete, TimingComplete: s.TimingComplete, TimingLimited: s.TimingLimited, Malformed: s.Malformed, Oversized: s.Oversized, MissingRecords: s.MissingRecords, Status: "idle"}
	if x.HasUsage && s.HasCache {
		n := s.Usage.Input - s.Usage.Cached
		x.Uncached = &n
	}
	if x.HasUsage && s.HasCache && s.Usage.Input > 0 {
		n := 100 * float64(s.Usage.Cached) / float64(s.Usage.Input)
		x.CachePercent = &n
	}
	if s.TTFTCount > 0 {
		n := float64(s.TTFTSum) / float64(s.TTFTCount)
		x.AverageTTFT = &n
	}
	x.DecodeTokens, x.DecodeMS = s.DecodeTokens, s.DecodeMS
	x.DecodeSamples, x.DecodeSkipped = s.DecodeSamples, s.DecodeSkipped
	if s.DecodeMS > 0 && s.DecodeSamples > 0 {
		n := float64(s.DecodeTokens) * 1000 / float64(s.DecodeMS)
		x.TPS, x.TPSApproximate = &n, true
	}
	if t := s.Turns[s.CurrentID]; t != nil {
		x.Status = "waiting"
		if t.Complete {
			x.Status = "completed"
		}
		if !t.Complete && len(t.Model) > 0 {
			x.Status = "generating"
		}
	}
	return x
}

func Compact(n int64) string {
	return compactNumber(n, false, false)
}

// CompactLowerBound never rounds a qualified count above the observed value.
func CompactLowerBound(n int64) string {
	return compactNumber(n, true, false)
}

func Number(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func Duration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm %02ds", ms/60000, ms/1000%60)
}

func DurationLowerBound(ms int64) string {
	if ms >= 1000 && ms < 60000 {
		return fmt.Sprintf("%d.%d s", ms/1000, ms%1000/100)
	}
	return Duration(ms)
}

const MaxIntervals = 8192

func (s *State) makeRoom(current *Turn) {
	if s.retainedIntervals < MaxIntervals {
		return
	}
	for _, id := range s.Order {
		t := s.Turns[id]
		if t == nil || t == current || !t.Complete || t.Retired {
			continue
		}
		s.retainedIntervals -= len(t.Model) + len(t.Tools)
		t.Model = nil
		t.Tools = nil
		t.Retired = true
		if s.retainedIntervals < MaxIntervals {
			return
		}
	}
}

// ValidateBudget runs before cache encoding, so invalid/oversized state is never
// fully marshalled just to discover its size. Rebuild private accounting on load.
func (s *State) ValidateBudget() bool {
	if s == nil || len(s.Turns) > 32 || len(s.Order) != len(s.Turns) {
		return false
	}
	seen := make(map[string]bool, len(s.Order))
	for _, id := range s.Order {
		if s.Turns[id] == nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	intervals := 0
	for id, t := range s.Turns {
		if len(id) > 256 || t == nil {
			return false
		}
		intervals += len(t.Model) + len(t.Tools)
		if intervals > MaxIntervals {
			return false
		}

	}
	s.retainedIntervals = intervals
	return true
}

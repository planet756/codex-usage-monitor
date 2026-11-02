// Package status records only the overlay's visibility and native-call state.
// It deliberately has no fields for chat content, conversation IDs, or credentials.
package status

import (
	"codex-usage-monitor/internal/storage"
	"encoding/json"
	"time"

	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/rollout"
)

type Report struct {
	UpdatedAt            time.Time          `json:"updated_at"`
	DataObservedAt       time.Time          `json:"data_observed_at,omitzero"`
	ProcessID            uint32             `json:"process_id"`
	HostPID              uint32             `json:"host_pid"`
	HostWindow           uint64             `json:"host_window"`
	ForegroundWindow     uint64             `json:"foreground_window"`
	ForegroundPID        uint32             `json:"foreground_pid"`
	Visible              bool               `json:"visible"`
	NativeVisible        bool               `json:"native_visible"`
	ActualBounds         layout.Rect        `json:"actual_bounds"`
	HostTopmost          bool               `json:"host_topmost"`
	OverlayTopmost       bool               `json:"overlay_topmost"`
	Cloaked              bool               `json:"cloaked"`
	OpaquePixels         int                `json:"opaque_pixels"`
	SampleWindow         uint64             `json:"sample_window,omitempty"`
	SamplePID            uint32             `json:"sample_pid,omitempty"`
	SampleClass          string             `json:"sample_class,omitempty"`
	SampleHitsOverlay    bool               `json:"sample_hits_overlay"`
	Reason               string             `json:"reason"`
	DPI                  int                `json:"dpi"`
	Bounds               layout.Rect        `json:"bounds"`
	PlacementMode        string             `json:"placement_mode"`
	CaptionBounds        layout.Rect        `json:"caption_bounds"`
	CaptionSource        string             `json:"caption_source"`
	CaptionProbe         CaptionProbe       `json:"caption_probe"`
	OwnerLinked          bool               `json:"owner_linked"`
	HasUsage             bool               `json:"has_usage"`
	UsageStale           bool               `json:"usage_stale"`
	HasTiming            bool               `json:"has_timing"`
	DataMessage          string             `json:"data_message,omitempty"` // Fixed reader status text, never chat content.
	DataLoading          bool               `json:"data_loading"`
	RecoveredBoundaries  int                `json:"history_offsets_recovered,omitempty"`
	IPCConnected         bool               `json:"ipc_connected"`
	LiveAvailable        bool               `json:"live_available"`
	LiveFrames           int64              `json:"live_frames"`
	LiveRevision         int64              `json:"live_revision"`
	LiveResyncs          int64              `json:"live_resyncs"`
	SkippedFrames        int64              `json:"skipped_frames"`
	LiveUnsupported      bool               `json:"live_unsupported"`
	BranchVerified       bool               `json:"desktop_branch_verified"`
	SourcePathMapped     bool               `json:"source_path_mapped"`
	SourceKind           string             `json:"source_kind,omitempty"`
	ViewSource           string             `json:"view_source"`
	ViewReason           string             `json:"view_reason"`
	ChatPage             bool               `json:"chat_page"`
	ViewSelectedElements int                `json:"view_selected_elements"`
	ViewPageRequired     bool               `json:"view_page_evidence_required"`
	ViewHeaderProbe      [3]int             `json:"view_header_probe"` // Counts only; no titles, IDs or editor values.
	PopupAvailable       bool               `json:"popup_detection_available"`
	PopupHidden          bool               `json:"popup_hiding_overlay"`
	PopupSource          string             `json:"popup_source"`
	TPS                  *float64           `json:"tps,omitempty"`
	TurnCount            int64              `json:"turns"`
	StepCount            int64              `json:"steps"`
	CountsKnown          bool               `json:"counts_known"`
	CountsComplete       bool               `json:"counts_complete"`
	TPSApproximate       bool               `json:"tps_approximate"`
	DecodeMS             int64              `json:"decode_ms"`
	DecodeSamples        int64              `json:"decode_samples"`
	DecodeSkipped        int64              `json:"decode_skipped"`
	TimingComplete       bool               `json:"timing_complete"`
	TimingLimited        bool               `json:"timing_limited"`
	OwnerVerified        bool               `json:"source_owner_verified"`
	HistoryComplete      bool               `json:"history_complete"`
	Malformed            int64              `json:"malformed_records"`
	Oversized            int64              `json:"oversized_records"`
	MissingRecords       int64              `json:"missing_records"`
	Error                string             `json:"error,omitempty"`
	Load                 rollout.LoadTiming `json:"load,omitzero"`
}

// CaptionProbe records numeric native API results only. It helps distinguish
// unsupported system captions from rejected bounds or an unresponsive host.
type CaptionProbe struct {
	TitlebarOK     bool        `json:"titlebar_ok"`
	TitlebarBounds layout.Rect `json:"titlebar_bounds"`
	TitlebarState  uint32      `json:"titlebar_state"`
	DWMResult      int32       `json:"dwm_result"`
	DWMBounds      layout.Rect `json:"dwm_bounds"`
	HitTestQueries int         `json:"hit_test_queries"`
	HitTestCodes   uint32      `json:"hit_test_codes"`
	HitTestError   uint32      `json:"hit_test_error"`
	BudgetExceeded bool        `json:"budget_exceeded"`
}

func Write(path string, r Report) error {
	r.UpdatedAt = time.Now().UTC()
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return storage.WriteAtomic(path, append(b, '\n'))
}

func Description(reason string) string {
	switch reason {
	case "visible":
		return "Visible"
	case "host_not_found":
		return "Codex main window not found"
	case "caption_not_found":
		return "Could not position the overlay"
	case "owner_link_failed":
		return "Could not link the overlay to the Codex window"
	case "host_hidden":
		return "Codex is hidden or minimized"
	case "popup_open":
		return "Codex popup or search is open; statistics continue"
	case "non_chat_page":
		return "No visible chat page"
	case "geometry_failed":
		return "Could not read window bounds"
	case "insufficient_space":
		return "Window is too narrow for the overlay"
	case "paint_failed":
		return "Could not draw the overlay"
	case "show_failed":
		return "Windows could not show the overlay"
	default:
		return "Waiting for overlay status"
	}
}

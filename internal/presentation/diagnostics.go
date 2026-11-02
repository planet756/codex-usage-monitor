package presentation

import (
	"fmt"
	"strings"

	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/status"
)

func Diagnostics(language string, r status.Report) string {
	tr := func(s string) string { return i18n.Text(language, s) }
	var lines []string
	add := func(label string, value any) {
		lines = append(lines, tr(label)+": "+fmt.Sprint(value))
	}
	display := status.Description(r.Reason)
	if r.Reason == "visible" && (!r.Visible || !r.NativeVisible || r.Cloaked) {
		display = "Not visible"
	}
	add("Display", tr(display))
	connection := "Reconnecting"
	if r.LiveUnsupported {
		connection = "Desktop protocol unsupported"
	} else if r.IPCConnected {
		connection = "Connected"
	}
	add("Connection", tr(connection))
	// Connection health does not establish that the selected chat's log is ready.
	// Surface data issues, never repeat the chat's accounting statistics.
	data := r.DataMessage
	if data == "Waiting for Desktop connection" {
		data = ""
	}
	if data == "" && r.IPCConnected && !r.LiveUnsupported {
		switch {
		case r.DataLoading:
			data = "Loading current chat…"
		case r.UsageStale:
			data = "Latest token usage unavailable"
		case r.ViewReason == "selected_chat_ambiguous":
			data = "Current chat identity is ambiguous"
		case r.ViewReason == "chat_catalog_unavailable":
			data = "Chat catalog unavailable · retrying"
		case r.ViewReason == "pending_chat_identity":
			data = "New chat · waiting for chat ID"
		case r.DataMessage != "" && r.DataMessage != "Waiting for Desktop connection":
			data = r.DataMessage
		case !r.HasUsage && !r.CountsKnown:
			data = "Resolving the selected chat"
			if r.ViewReason == "verified" {
				data = "Waiting for selected chat data"
			}
		case r.TimingLimited:
			data = "Timing detail limit"
		case !r.HistoryComplete:
			data = "Partial history"
		case r.DecodeSamples == 0 && r.DecodeSkipped > 0:
			data = "Timing unavailable"
		}
	}
	if data != "" || r.Malformed > 0 || r.Oversized > 0 || strings.TrimSpace(r.Error) != "" {
		lines = append(lines, "")
	}
	if data != "" {
		add("Data", tr(data))
	}
	if r.Load.Mode != "" {
		add("History load", tr(r.Load.Mode))
		add("Locate / restore", fmt.Sprintf("%d / %d ms", r.Load.LocateMS, r.Load.RestoreMS))
		add("Read / verify", fmt.Sprintf("%d / %d ms", r.Load.ReadMS, r.Load.VerifyMS))
		if r.Load.DeliveryMS > 0 {
			add("First counts", fmt.Sprintf("%d ms", r.Load.DeliveryMS))
		}
		if r.Load.ReadyMS > 0 {
			add("History ready", fmt.Sprintf("%d ms", r.Load.ReadyMS))
		}
	}
	if r.RecoveredBoundaries > 0 {
		add("History", i18n.Format(language, "%d reference offsets verified by record sequence", r.RecoveredBoundaries))
	}
	if r.MissingRecords > 0 {
		add("History", i18n.Format(language, "%d missing records · statistics incomplete", r.MissingRecords))
	}
	if r.Malformed > 0 {
		add("Malformed records", r.Malformed)
	}
	if r.Oversized > 0 {
		add("Oversized records skipped", r.Oversized)
	}
	if detail := strings.TrimSpace(r.Error); detail != "" {
		add("Error", tr(detail))
	}
	return strings.Join(lines, "\n")
}

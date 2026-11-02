package win

import (
	"codex-usage-monitor/internal/ipc"
	"time"
)

func (a *overlay) setView(v viewSelection) {
	if a.popup.Hidden && v.Window == a.viewResult.Window && v.ID == "" && a.viewResult.ID != "" {
		return // Modal accessibility must not cancel background accounting.
	}
	if v == a.viewResult {
		return
	}
	previousHost := a.viewResult.Host
	a.viewResult = v
	// Fence presentation immediately as well as the IPC subscription. Delayed
	// reader updates must never repaint the previous chat after a UI switch.
	if a.data.ID != v.ID || previousHost != v.Host {
		a.data = dataUpdate{ID: v.ID, Host: v.Host, Message: ipc.ViewMessage(v.Reason)}
		a.dirty = true
		a.collapse()
	}
	select {
	case a.viewSelections <- v:
	default:
		select {
		case <-a.viewSelections:
		default:
		}
		select {
		case a.viewSelections <- v:
		default:
		}
	}
}

func (a *overlay) requestView(urgent bool) {
	if a.viewResult.Window != a.host {
		a.viewEpoch++
		a.selectionDirty = true
		a.setView(viewSelection{Window: a.host, Reason: "resolving_selected_chat"})
	}
	if a.viewPending {
		if time.Since(a.viewRequested) > 3*time.Second {
			a.setView(viewSelection{Window: a.host, Reason: "uia_query_timeout"})
		}
		return
	}
	interval := 2 * time.Second
	if !a.viewEvents {
		interval = 500 * time.Millisecond
	}
	if a.popup.Hidden {
		interval = 250 * time.Millisecond
	}
	if urgent {
		interval = 100 * time.Millisecond
	}
	if !a.selectionDirty && a.viewRequestHost == a.host && time.Since(a.viewRequested) < interval {
		return
	}
	select {
	case a.viewRequests <- viewRequest{Window: a.host, Epoch: a.viewEpoch, Selection: a.selectionDirty, Refresh: urgent}:
		a.viewPendingEpoch = a.viewEpoch
		a.selectionDirty = false
		a.viewPending, a.viewRequestHost, a.viewRequested = true, a.host, time.Now()
	default:
	}
}

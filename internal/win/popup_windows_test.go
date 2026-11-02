package win

import (
	"testing"

	"codex-usage-monitor/internal/metrics"
	popupstate "codex-usage-monitor/internal/popup"
)

// This test exercises the subscription/display boundary. It is compiled during
// cross-validation; execution needs native Windows because this package has
// native callback initializers.
func TestPopupCannotClearCurrentChatSubscriptionOrSettledUsage(t *testing.T) {
	const id = "01a15da4-16e9-7951-8fce-50f7bef8bb83"
	rate := 80.0
	a := overlay{
		host:           1234,
		viewResult:     viewSelection{Window: 1234, ID: id, Source: "uia_document_url", Reason: "verified"},
		viewSelections: make(chan viewSelection, 1),
		data:           dataUpdate{ID: id, Snapshot: metrics.Snapshot{ThreadID: id, HasUsage: true, TPS: &rate, DecodeSamples: 10}},
		popup:          popupstate.State{Window: 1234, Hidden: true},
	}
	a.setView(viewSelection{Window: 1234, Reason: "selected_chat_unavailable"})
	if a.viewResult.ID != id || a.data.ID != id || a.data.Snapshot.DecodeSamples != 10 || *a.data.Snapshot.TPS != 80 {
		t.Fatal("modal accessibility cleared a verified chat or its settled data")
	}
	select {
	case <-a.viewSelections:
		t.Fatal("dialog caused a subscription change")
	default:
	}
	// Verified information is still accepted while the overlay is hidden.
	a.setView(viewSelection{Window: 1234, ID: id, Source: "uia_catalog_title", Reason: "verified"})
	if got := <-a.viewSelections; got.ID != id || got.Source != "uia_catalog_title" {
		t.Fatal("hidden overlay discarded a verified selection")
	}
}

func TestNativeFindBarHidesWithoutModalRoleOrKeyboardFocus(t *testing.T) {
	root, input := pageFixture(t)
	selectionFixture.rows = []*comObject{input}
	selectionFixture.controlTypes[input] = 50004
	selectionFixture.automationIDs[input] = popupstate.FindInputPrefix + "01a15da4-16e9-7951-8fce-50f7bef8bb83"
	p := popupLocator{condition: root, cache: root}
	defer p.close()
	for range 3 {
		got := p.locate(root, 0, root)
		if !got.Available || !got.Visible || got.Source != "uia_find_bar" {
			t.Fatalf("search without dialog role: %+v", got)
		}
	}
	selectionFixture.automationIDs[input] += "-unrelated"
	if got := p.locate(root, 0, root); !got.Available || got.Visible {
		t.Fatalf("invalid ID hidden: %+v", got)
	}
	delete(selectionFixture.automationIDs, input)
	if got := p.locate(root, 0, root); got.Available || got.Source != "uia_popup_query_failed" {
		t.Fatalf("missing metadata proved closure: %+v", got)
	}
	selectionFixture.rows = nil
	if got := p.locate(root, 0, root); !got.Available || got.Visible || got.Source != "clear" {
		t.Fatalf("closed search: %+v", got)
	}
	selectionFixture.rows = make([]*comObject, 33)
	for i := range selectionFixture.rows {
		selectionFixture.rows[i] = input
	}
	if got := p.locate(root, 0, root); got.Available || got.Visible || got.Source != "uia_popup_query_failed" {
		t.Fatalf("truncated query treated as closed: %+v", got)
	}
}

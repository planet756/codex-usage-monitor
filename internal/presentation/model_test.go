package presentation

import (
	"codex-usage-monitor/internal/metrics"
	"testing"
)

func TestDisplayEqualityUsesVisibleValuesNotPointersOrClosedDetails(t *testing.T) {
	rate := 80.0
	a := metrics.Snapshot{TPS: &rate, HasCounts: true, Turns: 1, Steps: 2}
	first := Display(a, "en", "chat-a", "", false, false, false)
	other := 80.0
	a.TPS = &other
	a.DecodeSkipped = 20
	a.ModelMS = 100000
	if Display(a, "en", "chat-a", "", false, false, false) != first {
		t.Fatal("unchanged strip was invalidated by pointers or hidden details")
	}
	if Display(a, "en", "chat-a", "", false, false, true) == Display(metrics.Snapshot{TPS: &rate, HasCounts: true, Turns: 1, Steps: 2}, "en", "chat-a", "", false, false, true) {
		t.Fatal("expanded details ignored a changed coverage value")
	}
	a.Steps++
	if Display(a, "en", "chat-a", "", false, false, false) == first {
		t.Fatal("visible step change was lost")
	}
}

func TestEmptyChatKeepsAllFieldsThroughIdentityAndConnectionStates(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		base := Display(metrics.Snapshot{}, language, "", "", false, false, false)
		wantCounts := "- turns · - steps"
		if language == "zh-CN" {
			wantCounts = "- 轮 · - 步"
		}
		if base.Counts != wantCounts {
			t.Fatalf("empty chat lost count fields: %q", base.Counts)
		}
		for _, part := range base.Parts {
			if part.Value != "-" || part.Unit == "" {
				t.Fatalf("missing value or field label lost: %+v", part)
			}
		}
		for _, message := range []string{"New chat · waiting for chat ID", "Waiting for current chat identity", "Waiting for Desktop connection", "Current chat identity is ambiguous", "Current chat log not found · retrying"} {
			for _, id := range []string{"", "bound-chat"} {
				for _, loading := range []bool{false, true} {
					if got := Display(metrics.Snapshot{}, language, id, message, loading, false, false); got != base {
						t.Fatalf("pending state replaced metric fields: %q %+v", message, got)
					}
					m := Display(metrics.Snapshot{}, language, id, message, loading, false, true)
					wantCoverage := message
					if loading {
						wantCoverage = "Loading chat history…"
					}
					if m.Coverage != wantCoverage {
						t.Fatalf("diagnostic reason lost: %+v", m)
					}
					for _, row := range m.Rows {
						if row.Label == "" || row.Value != "-" {
							t.Fatalf("empty detail field lost: %+v", row)
						}
					}
				}
			}
		}
		// Real zero usage is data and must not remain a placeholder after binding.
		zero := metrics.Snapshot{HasCounts: true, CountsComplete: true}.WithUsage(metrics.Usage{}, true)
		m := Display(zero, language, "bound-chat", "", false, false, false)
		if m.Parts[1].Value != "0" || m == base {
			t.Fatalf("bound chat did not show real zero: %+v", m)
		}
		// Returning from a populated chat must restore placeholders, not reuse
		// the previous chat's values or a full-width waiting message.
		populated := zero.WithUsage(metrics.Usage{Input: 100, Cached: 25, Output: 20}, true)
		populated.Turns, populated.Steps, populated.TPS = 2, 7, new(80.0)
		active := Display(populated, language, "bound-chat", "", false, false, false)
		if active.Parts[0].Value != "80.0" || active.Parts[1].Value != "120" || active.Parts[2].Value != "25.00%" || active == base {
			t.Fatalf("binding did not restore real statistics: %+v", active)
		}
		if Display(metrics.Snapshot{}, language, "", "Waiting for current chat identity", false, false, false) != base {
			t.Fatal("new chat did not clear the previous display")
		}
	}
}

func TestWidthCacheInvalidatesWhenEqualRoundedRatesChangeFallback(t *testing.T) {
	for _, rates := range [][2]float64{{999.6, 1000.4}, {1049.6, 1050.4}, {1499.6, 1500.4}} {
		a, b := rates[0], rates[1]
		s := metrics.Snapshot{TPS: &a}
		before := Display(s, "en", "chat", "", false, false, false)
		s.TPS = &b
		after := Display(s, "en", "chat", "", false, false, false)
		if before.Parts[0] != after.Parts[0] || before == after || before.RateFallbacks == after.RateFallbacks {
			t.Fatalf("rate fallback lost from layout cache key: %v", rates)
		}
	}
}

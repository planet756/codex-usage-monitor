package win

import (
	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/metrics"
	"testing"
)

func TestPaintKeyIgnoresTransportDiagnosticsAndClosedDetails(t *testing.T) {
	a := overlay{cfg: config.Default(), dpi: 96}
	a.data.Snapshot = metrics.Snapshot{HasCounts: true, Turns: 1, Steps: 5}
	key := a.paintKey(440, 26, 96)
	for range 100 {
		a.viewResult.HeaderProbe[0]++
		a.viewResult.PageEvidenceRequired = !a.viewResult.PageEvidenceRequired
		a.data.Live.Frames++
		a.data.Live.Revision++
		a.data.Snapshot.ModelMS++
		if a.paintKey(440, 26, 96) != key {
			t.Fatal("invisible data invalidated actual native paint key")
		}
	}
	a.data.Snapshot.Steps++
	if a.paintKey(440, 26, 96) == key {
		t.Fatal("visible response count did not invalidate native paint key")
	}
}

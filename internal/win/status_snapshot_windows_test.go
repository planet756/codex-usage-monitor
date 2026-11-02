package win

import (
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/status"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeStatusSnapshotTracksMetricsAndGeneration(t *testing.T) {
	dir := t.TempDir()
	a := &overlay{configPath: filepath.Join(dir, "settings.json"), data: dataUpdate{Generation: 1, Snapshot: metrics.Snapshot{HasCounts: true, CountsComplete: true, HistoryComplete: true, TimingComplete: true, Turns: 1, Steps: 2}}}
	a.recordStatus("host_hidden", nil)
	a.data.Snapshot.Steps = 3
	a.recordStatus("host_hidden", nil)
	a.statusWritten = time.Now().Add(-3 * time.Second)
	a.recordStatus("host_hidden", nil)
	var report status.Report
	b, err := os.ReadFile(filepath.Join(dir, "overlay-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &report); err != nil || report.StepCount != 3 {
		t.Fatal("unchanged health retained old statistics", err)
	}
	a.data.Generation++
	a.data.Snapshot.Steps = 4
	a.recordStatus("host_hidden", nil)
	b, _ = os.ReadFile(filepath.Join(dir, "overlay-status.json"))
	json.Unmarshal(b, &report)
	if report.StepCount != 4 {
		t.Fatal("new source generation was throttled")
	}
	a.data.Snapshot.MissingRecords = 2
	a.recordStatus("host_hidden", nil)
	b, _ = os.ReadFile(filepath.Join(dir, "overlay-status.json"))
	json.Unmarshal(b, &report)
	if report.MissingRecords != 2 {
		t.Fatal("new missing-history diagnostic was not written")
	}
}

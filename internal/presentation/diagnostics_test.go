package presentation

import (
	"strings"
	"testing"

	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/status"
)

func TestStatusShowsOnlyConnectionAndDisplayWhenHealthy(t *testing.T) {
	rate := 35.6
	r := status.Report{Reason: "visible", Visible: true, NativeVisible: true, IPCConnected: true,
		CountsKnown: true, CountsComplete: true, TurnCount: 12, StepCount: 133,
		TPS: &rate, TPSApproximate: true, DecodeMS: 27062, DecodeSamples: 122, DecodeSkipped: 11, HistoryComplete: true, TimingComplete: true,
		ViewSource: "internal-source", CaptionSource: "internal-caption"}
	for _, tc := range []struct {
		lang string
		want []string
	}{
		{"en", []string{"Display: Visible", "Connection: Connected"}},
		{"zh-CN", []string{"显示: 已显示", "连接: 已连接"}},
	} {
		got := Diagnostics(tc.lang, r)
		for _, line := range tc.want {
			if !strings.Contains(got, line) {
				t.Fatalf("%s status lost %q: %s", tc.lang, line, got)
			}
		}
		if got != strings.Join(tc.want, "\n") {
			t.Fatalf("accounting or debug fields leaked into status: %s", got)
		}
	}
}

func TestStatusShowsLoadStagesWithoutChatStatistics(t *testing.T) {
	r := status.Report{IPCConnected: true, HasUsage: true, HistoryComplete: true, TimingComplete: true, Load: rollout.LoadTiming{Mode: "prefix", LocateMS: 50, RestoreMS: 20, ReadMS: 30, VerifyMS: 10, DeliveryMS: 120, ReadyMS: 150}}
	for lang, parts := range map[string][]string{"en": {"History load: prefix", "Locate / restore: 50 / 20 ms", "Read / verify: 30 / 10 ms", "First counts: 120 ms", "History ready: 150 ms"}, "zh-CN": {"历史加载: 历史前缀", "定位 / 恢复: 50 / 20 ms", "首次计数: 120 ms"}} {
		got := Diagnostics(lang, r)
		for _, part := range parts {
			if !strings.Contains(got, part) {
				t.Fatalf("missing stage %q", part)
			}
		}
		if strings.Contains(got, "turns") || strings.Contains(got, "steps") || strings.Contains(got, "tok/s") {
			t.Fatal("repeated chat statistics in status")
		}
	}
}

func TestRecoveredHistoryOffsetsAreInformationalAndLocalized(t *testing.T) {
	r := status.Report{Reason: "visible", Visible: true, NativeVisible: true, IPCConnected: true, HasUsage: true, HistoryComplete: true, TimingComplete: true, RecoveredBoundaries: 35}
	for lang, want := range map[string]string{"en": "History: 35 reference offsets verified by record sequence", "zh-CN": "历史: 已按记录序号核验 35 处引用位置"} {
		got := Diagnostics(lang, r)
		if !strings.Contains(got, want) || strings.Contains(got, "Data:") || strings.Contains(got, "Error:") || strings.Contains(got, "历史引用边界无效") {
			t.Fatalf("recovered history was hidden or reported as an error: %s", got)
		}
	}
}

func TestStatusShowsMissingHistoryInBothLanguages(t *testing.T) {
	r := status.Report{IPCConnected: true, HasUsage: true, CountsKnown: true, MissingRecords: 3}
	for lang, want := range map[string]string{
		"en":    "History: 3 missing records · statistics incomplete",
		"zh-CN": "历史: 缺少 3 条记录 · 统计不完整",
	} {
		got := Diagnostics(lang, r)
		if !strings.Contains(got, want) {
			t.Fatalf("missing history was not explained: %s", got)
		}
	}
}

func TestStatusRetainsConnectionAndDataProblemsWithoutAccounting(t *testing.T) {
	r := status.Report{Reason: "visible"}
	got := Diagnostics("en", r)
	for _, line := range []string{"Display: Not visible", "Connection: Reconnecting"} {
		if !strings.Contains(got, line) {
			t.Fatalf("missing state concealed: %s", got)
		}
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("missing data fabricated zeros or warnings: %s", got)
	}
	r.TPS = new(0.0)
	r.DecodeMS, r.DecodeSamples, r.DecodeSkipped = 1000, 1, 2
	r.LiveUnsupported, r.Malformed, r.Oversized = true, 3, 1
	r.DataMessage = "Chat ID mismatch · usage withheld"
	r.Error = "Chat log unavailable · retrying"
	got = Diagnostics("en", r)
	for _, line := range []string{"Connection: Desktop protocol unsupported", "Data: Chat ID mismatch · usage withheld",
		"Malformed records: 3", "Oversized records skipped: 1", "Error: Chat log unavailable · retrying"} {
		if !strings.Contains(got, line) {
			t.Fatalf("useful state lost: %s", got)
		}
	}
	for _, removed := range []string{"TPS:", "TPS samples:", "Decode time:", "turns", "steps"} {
		if strings.Contains(got, removed) {
			t.Fatalf("accounting leaked into error state: %s", got)
		}
	}
	if got := Diagnostics("zh-CN", r); !strings.Contains(got, "错误: 会话日志不可用 · 正在重试") {
		t.Fatalf("error not localized: %s", got)
	}
}

func TestStatusExplainsDataAvailabilityOnlyWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report status.Report
		want   string
	}{
		{"resolving", status.Report{IPCConnected: true}, "Resolving the selected chat"},
		{"verified pending", status.Report{IPCConnected: true, ViewReason: "verified"}, "Waiting for selected chat data"},
		{"ambiguous", status.Report{IPCConnected: true, ViewReason: "selected_chat_ambiguous"}, "Current chat identity is ambiguous"},
		{"catalog unavailable", status.Report{IPCConnected: true, ViewReason: "chat_catalog_unavailable"}, "Chat catalog unavailable · retrying"},
		{"new chat", status.Report{IPCConnected: true, ViewReason: "pending_chat_identity"}, "New chat · waiting for chat ID"},
		{"loading seed", status.Report{IPCConnected: true, DataLoading: true, HasUsage: true}, "Loading current chat…"},
		{"incomplete", status.Report{IPCConnected: true, HasUsage: true}, "Partial history"},
		{"missing timing", status.Report{IPCConnected: true, HasUsage: true, HistoryComplete: true, TimingComplete: true, DecodeSkipped: 3}, "Timing unavailable"},
		{"latest usage missing", status.Report{IPCConnected: true, CountsKnown: true, UsageStale: true, HistoryComplete: true, TimingComplete: true}, "Latest token usage unavailable"},
		{"reader error while disconnected", status.Report{DataMessage: "Chat log unavailable · retrying"}, "Chat log unavailable · retrying"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnostics("en", tc.report)
			if !strings.Contains(got, "Data: "+tc.want) || strings.Count(got, "Data:") != 1 {
				t.Fatalf("missing or redundant data notice: %s", got)
			}
		})
	}
	// Older report files lack the new data-message/loading fields. Loaded usage
	// and a complete history still establish a healthy data state.
	got := Diagnostics("en", status.Report{IPCConnected: true, HasUsage: true, HistoryComplete: true, TimingComplete: true})
	if strings.Contains(got, "Data:") {
		t.Fatalf("healthy older report fabricated a data issue: %s", got)
	}
}

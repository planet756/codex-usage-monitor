package presentation

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
	"codex-usage-monitor/internal/status"
)

func TestLogProblemStageLocalizationAndDisconnectedStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{rollout.ErrHomeUnavailable, "Codex data folder unavailable · check codex_home"},
		{rollout.ErrLogNotFound, "Current chat log not found · retrying"},
		{rollout.ErrLogAmbiguous, "Multiple history branches · history withheld"},
		{rollout.ErrBranchUnverified, "Waiting for Desktop current history branch"},
		{rollout.ErrHistoryMissing, "Referenced history missing · history withheld"},
		{rollout.ErrArchiveInvalid, "Compressed history invalid or exceeds limits · history withheld"},
		{rollout.ErrHistoryBoundary, "History reference boundary invalid · history withheld"},
		{rollout.ErrHistoryCycle, "History reference cycle or limit reached · history withheld"},
		{rollout.ErrSourceBusy, "Verifying current history…"},
		{rollout.ErrHistoryChanged, "History changed · rebuilding"},
		{rollout.ErrHistoryOrdinal, "History record sequence invalid · history withheld"},
		{rollout.ErrAccountingConflict, "Conflicting response accounting · history withheld"},
		{rollout.ErrChatMismatch, "Chat ID mismatch · usage withheld"},
		{rollout.ErrLogIdentityUnverified, "Chat log identity unverified · history withheld"},
		{rollout.ErrLogPending, "Chat log is being created · retrying"},
		{rollout.ErrLogSearchIncomplete, "Chat log search incomplete · check access"},
		{rollout.ErrLogUnreadable, "Chat log unreadable · check access"},
		{rollout.ErrLogPathUnsupported, "Chat log path is not Windows-readable"},
		{source.ErrHostUnsupported, "Current chat log is not on this Windows host"},
		{os.ErrPermission, "Chat log unreadable · check access"},
		{os.ErrNotExist, "Current chat log not found · retrying"},
		{errors.New("private details"), "Chat log unavailable · retrying"},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			message := LogProblem(fmt.Errorf("private file path: %w", tc.err))
			if message != tc.want || strings.Contains(message, "private") {
				t.Fatalf("wrong/sensitive diagnostic: %s", message)
			}
			for _, connected := range []bool{true, false} {
				r := status.Report{IPCConnected: connected, DataMessage: message}
				english, chinese := Diagnostics("en", r), Diagnostics("zh-CN", r)
				if !strings.Contains(english, "Data: "+message) || strings.Count(english, "Data:") != 1 || !strings.Contains(chinese, "数据: ") || strings.Contains(chinese, message) {
					t.Fatalf("data issue lost or untranslated: %s / %s", english, chinese)
				}
			}
		})
	}
}

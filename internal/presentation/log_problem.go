package presentation

import (
	"errors"
	"os"

	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

// LogProblem reports the failed stage without leaking raw paths, database
// messages or file contents, or blaming codex_home for every discovery error.
func LogProblem(err error) string {
	switch {
	case errors.Is(err, source.ErrHostUnsupported):
		return "Current chat log is not on this Windows host"
	case errors.Is(err, rollout.ErrBranchUnverified):
		return "Waiting for Desktop current history branch"
	case errors.Is(err, rollout.ErrArchiveInvalid):
		return "Compressed history invalid or exceeds limits · history withheld"
	case errors.Is(err, rollout.ErrAccountingConflict):
		return "Conflicting response accounting · history withheld"
	case errors.Is(err, rollout.ErrHistoryMissing):
		return "Referenced history missing · history withheld"
	case errors.Is(err, rollout.ErrHistoryBoundary):
		return "History reference boundary invalid · history withheld"
	case errors.Is(err, rollout.ErrHistoryCycle):
		return "History reference cycle or limit reached · history withheld"
	case errors.Is(err, rollout.ErrSourceBusy):
		return "Verifying current history…"
	case errors.Is(err, rollout.ErrHistoryChanged):
		return "History changed · rebuilding"
	case errors.Is(err, rollout.ErrHistoryOrdinal):
		return "History record sequence invalid · history withheld"
	case errors.Is(err, rollout.ErrHomeUnavailable):
		return "Codex data folder unavailable · check codex_home"
	case errors.Is(err, rollout.ErrLogAmbiguous):
		return "Multiple history branches · history withheld"
	case errors.Is(err, rollout.ErrChatMismatch):
		return "Chat ID mismatch · usage withheld"
	case errors.Is(err, rollout.ErrLogIdentityUnverified):
		return "Chat log identity unverified · history withheld"
	case errors.Is(err, rollout.ErrLogPending):
		return "Chat log is being created · retrying"
	case errors.Is(err, rollout.ErrLogSearchIncomplete):
		return "Chat log search incomplete · check access"
	case errors.Is(err, rollout.ErrLogPathUnsupported):
		return "Chat log path is not Windows-readable"
	case errors.Is(err, rollout.ErrLogNotFound), errors.Is(err, os.ErrNotExist):
		return "Current chat log not found · retrying"
	case errors.Is(err, rollout.ErrLogUnreadable), errors.Is(err, os.ErrPermission):
		return "Chat log unreadable · check access"
	default:
		return "Chat log unavailable · retrying"
	}
}

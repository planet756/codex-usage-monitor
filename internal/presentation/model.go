package presentation

import "codex-usage-monitor/internal/metrics"

// Model contains only pixels' inputs. Transport revisions, counters and pointer
// identities cannot invalidate it; closed details do not participate in equality.
type Model struct {
	Counts             string
	Parts              [3]Metric
	RateFallbacks      [2]string
	Identity, Coverage string
	Rows               [9]DetailRow
}

func Display(s metrics.Snapshot, language, id, message string, loading, unsupported, expanded bool) Model {
	m := Model{Counts: Counts(s, language), Parts: Strip(s, language)}
	// Equal rounded rates can have different compact/scientific fallbacks.
	// Include the actual display alternatives in paint and width cache keys.
	m.RateFallbacks = stripRateFallbacks(s, m.Parts[0].Value)
	// The strip always shows its metric fields. Pending identity, connection
	// and data diagnostics belong in details/Status, never in place of values.
	if expanded {
		m.Identity = id
		copy(m.Rows[:], DetailRows(s, language, loading || message != ""))
		m.Coverage = SampleCoverage(s, language)
		if loading {
			m.Coverage = "Loading chat history…"
		} else if message != "" {
			m.Coverage = message
		}
		if unsupported {
			m.Coverage = "Desktop protocol unsupported"
		}
	}
	return m
}

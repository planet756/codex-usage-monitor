package monitor

import (
	"context"
	"io"

	"codex-usage-monitor/internal/source"
)

type View struct {
	Window                   uintptr
	ChatPage                 bool
	ID, Host, Source, Reason string
	SelectedElements         int
	PageEvidenceRequired     bool
	HeaderProbe              [3]int // Matching Chat scopes, title controls, unique catalog matches.
	epoch                    uint64
}

// Dependencies supplies only native transport, paths and file events. The
// connection, ownership, history verification and cancellation remain shared.
type Dependencies struct {
	Dial        func(context.Context) (io.ReadWriteCloser, error)
	IndexedPath func(context.Context, string, string, string) (string, error)
	Watch       func(context.Context, string, string, chan<- struct{}) error
	Resolve     func(string, source.Options) (source.Plan, error)
}

package win

import (
	"context"
	"errors"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

// Desktop's revision-checked path selects the file. Conversion does no I/O;
// Locator then verifies ownership, the selected branch and history boundaries.
func resolveActiveThread(ctx context.Context, home, host, id, activePath string, boundaries *rollout.BoundaryCache) (rollout.History, source.Plan, error) {
	if err := ctx.Err(); err != nil {
		return rollout.History{}, source.Plan{}, err
	}
	if activePath == "" {
		return rollout.History{}, source.Plan{}, rollout.ErrBranchUnverified
	}
	p, err := source.Resolve(activePath, source.Options{Home: home, Host: host})
	if errors.Is(err, source.ErrUnsupported) {
		err = rollout.ErrLogPathUnsupported
	}
	if err != nil {
		return rollout.History{}, source.Plan{}, err
	}
	h, err := (rollout.Locator{Home: p.Home, ActivePath: p.Path, Boundaries: boundaries}).Resolve(ctx, id)
	return h, p, err
}

func dataHome(cfg config.Config) string {
	raw := config.Home(cfg)
	if p, err := source.Directory(raw); err == nil {
		return p
	}
	return raw
}

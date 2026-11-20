package win

import (
	"context"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

func resolveActiveThread(ctx context.Context, home, host, id, activePath string, boundaries *rollout.BoundaryCache) (rollout.History, source.Plan, error) {
	return monitor.ResolveActiveThread(ctx, home, host, id, activePath, boundaries, source.Resolve)
}

func dataHome(cfg config.Config) string {
	raw := config.Home(cfg)
	if p, err := source.Directory(raw); err == nil {
		return p
	}
	return raw
}

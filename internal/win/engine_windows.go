package win

import (
	"context"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/source"
)

type dataUpdate = monitor.Update

func engine(ctx context.Context, cfg config.Config, configPath string, views <-chan viewSelection, publish func(dataUpdate)) {
	monitor.Run(ctx, dataHome(cfg), configPath, views, publish, monitor.Dependencies{
		Dial: dialIPC, IndexedPath: indexedRolloutPath, Watch: watchDirectory, Resolve: source.Resolve,
	})
}

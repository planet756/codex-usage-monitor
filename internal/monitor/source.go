package monitor

import (
	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
	"context"
	"errors"
)

// IndexedRolloutPath resolves a verified local chat against Codex's index.
// A route with no host needs an exact, unique local host in the UI catalog.
func IndexedRolloutPath(ctx context.Context, home, host, id string, query func(path, statement, key string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if host == "" {
		var err error
		host, err = query(source.JoinPath(home, "sqlite", "codex.db"), "SELECT CASE WHEN count(DISTINCT host_id)=1 THEN min(host_id) ELSE '' END FROM local_thread_catalog WHERE thread_id=?1 COLLATE NOCASE AND missing_candidate=0", id)
		if err != nil {
			return "", rollout.ErrStateIndexUnavailable
		}
		if host == "" {
			return "", rollout.ErrBranchUnverified
		}
	}
	if host != "local" {
		return "", source.ErrHostUnsupported
	}
	db, err := config.StateDatabase(home)
	if err != nil {
		return "", rollout.ErrStateIndexUnavailable
	}
	path, err := query(db, "SELECT rollout_path FROM threads WHERE id=?1 LIMIT 2", id)
	if err != nil {
		return "", rollout.ErrStateIndexUnavailable
	}
	if path == "" {
		return "", rollout.ErrLogNotFound
	}
	return path, ctx.Err()
}

// Codex's explicit path selects the file. Conversion does no I/O;
// Locator then verifies ownership, the selected branch and history boundaries.
func ResolveActiveThread(ctx context.Context, home, host, id, activePath string, boundaries *rollout.BoundaryCache, resolve func(string, source.Options) (source.Plan, error)) (rollout.History, source.Plan, error) {
	if err := ctx.Err(); err != nil {
		return rollout.History{}, source.Plan{}, err
	}
	if activePath == "" {
		return rollout.History{}, source.Plan{}, rollout.ErrBranchUnverified
	}
	p, err := resolve(activePath, source.Options{Home: home, Host: host})
	if errors.Is(err, source.ErrUnsupported) {
		err = rollout.ErrLogPathUnsupported
	}
	if err != nil {
		return rollout.History{}, source.Plan{}, err
	}
	h, err := (rollout.Locator{Home: p.Home, ActivePath: p.Path, Boundaries: boundaries}).Resolve(ctx, id)
	return h, p, err
}

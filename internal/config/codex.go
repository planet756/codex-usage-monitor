package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codex-usage-monitor/internal/source"
	"github.com/BurntSushi/toml"
)

// StateDatabase resolves the canonical Codex state DB using the user config
// and inherited environment. A relative environment override needs the
// backend's working directory, which cannot be inferred from the monitor.
func StateDatabase(home string) (string, error) {
	const maxConfig = 2 << 20
	var cfg struct {
		SQLiteHome *string `toml:"sqlite_home"`
	}
	f, err := os.Open(source.JoinPath(home, "config.toml"))
	if err == nil {
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
		if err != nil {
			return "", err
		}
		if len(raw) > maxConfig {
			return "", errors.New("Codex config exceeds size limit")
		}
		if _, err = toml.Decode(string(raw), &cfg); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir := home
	if cfg.SQLiteHome != nil {
		dir = *cfg.SQLiteHome
		if !filepath.IsAbs(dir) {
			if filepath.VolumeName(dir) != "" || len(dir) > 0 && os.IsPathSeparator(dir[0]) {
				return "", errors.New("Codex state directory depends on the backend's drive")
			}
			dir = source.JoinPath(home, dir)
		}
	} else if override := strings.TrimSpace(os.Getenv("CODEX_SQLITE_HOME")); override != "" {
		dir = override
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("Codex state directory is not absolute")
	}
	return source.JoinPath(dir, "state_5.sqlite"), nil
}

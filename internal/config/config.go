package config

import (
	"bytes"
	"codex-usage-monitor/internal/storage"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Config struct {
	CodexHome        string `json:"codex_home,omitempty"`
	Language         string `json:"language"`
	Theme            string `json:"theme"`
	StartWithWindows bool   `json:"start_with_windows"`
}

func Default() Config {
	return Config{Language: "en", Theme: "auto", StartWithWindows: true}
}

func DefaultPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	return filepath.Join(base, "CodexUsageMonitor", "settings.json")
}

func Home(c Config) string {
	if c.CodexHome != "" {
		return c.CodexHome
	}
	if s := os.Getenv("CODEX_HOME"); s != "" {
		return s
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

func Load(path string) (Config, error) {
	c := Default()
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil {
		return c, e
	}
	if len(b) > 64<<10 {
		return c, errors.New("settings exceed size limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid trailing settings data")
	}
	if c.Language != "en" && c.Language != "zh-CN" {
		return c, errors.New("invalid language: use en or zh-CN")
	}
	if c.Theme != "auto" && c.Theme != "dark" && c.Theme != "light" {
		return c, errors.New("invalid theme")
	}
	return c, nil
}

func Save(path string, c Config) error {
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return storage.WriteAtomic(path, append(b, '\n'))
}

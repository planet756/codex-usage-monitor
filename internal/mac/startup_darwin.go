package mac

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/storage"
)

func startupEnabled(c config.Config) bool { return c.StartAtLogin == nil || *c.StartAtLogin }
func startupFile() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "Library", "LaunchAgents", "com.codexusagemonitor.plist"), nil
}
func startupPlist(configPath string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	escape := func(s string) string { b.Reset(); _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.codexusagemonitor</string>
<key>ProgramArguments</key><array><string>%s</string><string>--watcher</string><string>--config</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
<key>ProcessType</key><string>Background</string>
</dict></plist>
`, escape(exe), escape(configPath))), nil
}
func registerStartup(configPath string, on bool) error {
	p, err := startupFile()
	if err != nil {
		return err
	}
	if !on {
		err = os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := startupPlist(configPath)
	if err != nil {
		return err
	}
	if current, e := os.ReadFile(p); e == nil && bytes.Equal(current, b) {
		return nil
	}
	return storage.WriteAtomic(p, b)
}
func ensureStartup(configPath string, c config.Config) error {
	return registerStartup(configPath, startupEnabled(c))
}
func Startup(configPath string, on bool) error {
	c, err := config.Load(configPath)
	if err != nil {
		return err
	}
	old := c
	c.StartAtLogin = &on
	if err = config.Save(configPath, c); err != nil {
		return err
	}
	if err = registerStartup(configPath, on); err != nil {
		return errors.Join(err, config.Save(configPath, old))
	}
	return nil
}
func StartupStatus(configPath string) (bool, error) {
	p, err := startupFile()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want, err := startupPlist(configPath)
	return bytes.Equal(b, want), err
}

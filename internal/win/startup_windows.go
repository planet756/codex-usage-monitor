package win

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codex-usage-monitor/internal/config"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "CodexUsageMonitor"

func StartupStatus(configPath string) (bool, error) {
	value, exists, e := startupRegistration()
	if e != nil || !exists {
		return false, e
	}
	want, e := startupCommand(configPath)
	return e == nil && strings.EqualFold(value, want), e
}

func startupRegistration() (string, bool, error) {
	k, e := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if e == registry.ErrNotExist {
		return "", false, nil
	}
	if e != nil {
		return "", false, e
	}
	defer k.Close()
	value, _, e := k.GetStringValue(runName)
	if e == registry.ErrNotExist {
		return "", false, nil
	}
	if e == registry.ErrUnexpectedType {
		return "", true, nil
	}
	return value, e == nil, e
}

func Startup(enable bool, configPath string) error {
	return config.UpdateStartup(configPath, enable, func(enabled bool) error {
		return registerStartup(enabled, configPath)
	})
}

func registerStartup(enable bool, configPath string) error {
	value, exists, e := startupRegistration()
	if e != nil {
		return e
	}
	if !enable {
		if !exists {
			return nil
		}
		k, e := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if e == registry.ErrNotExist {
			return nil
		}
		if e != nil {
			return e
		}
		defer k.Close()
		e = k.DeleteValue(runName)
		if e == registry.ErrNotExist {
			return nil
		}
		return e
	}
	command, e := startupCommand(configPath)
	if e != nil {
		return e
	}
	if exists && strings.EqualFold(value, command) {
		return nil
	}
	k, _, e := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if e != nil {
		return e
	}
	defer k.Close()
	return k.SetStringValue(runName, command)
}

func startupCommand(configPath string) (string, error) {
	exe, e := os.Executable()
	if e != nil {
		return "", e
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return "", e
	}
	configPath, e = filepath.Abs(configPath)
	if e != nil {
		return "", e
	}
	if strings.ContainsAny(exe+configPath, "\"\r\n") {
		return "", fmt.Errorf("invalid startup path")
	}
	return `"` + exe + `" --watcher --config "` + configPath + `"`, nil
}

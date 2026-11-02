package main

import (
	"encoding/json"
	"fmt"
	"os"

	"codex-usage-monitor/internal/win"
)

func prepareConsole()   { win.PrepareConsole() }
func showError(e error) { fmt.Fprintln(os.Stderr, e); win.ShowError(e.Error()) }
func platformRun(o options) error {
	if o.addStartMenu || o.removeStartMenu {
		if o.addStartMenu && o.removeStartMenu {
			return fmt.Errorf("choose add or remove Start menu shortcut")
		}
		return win.StartMenu(o.addStartMenu, o.configPath)
	}
	if o.install && o.uninstall {
		return fmt.Errorf("choose install or uninstall")
	}
	if o.install || o.uninstall {
		return win.Startup(o.install, o.configPath)
	}
	if o.startupStatus {
		on, e := win.StartupStatus(o.configPath)
		if e != nil {
			return e
		}
		fmt.Printf("Start with Windows: %t\n", on)
		return nil
	}
	if o.stop {
		return win.Stop()
	}
	if o.diagnostics {
		x, e := win.Diagnostics()
		if e != nil {
			return e
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(x)
	}
	if o.overlay {
		if o.pid == 0 || uint64(o.pid) > 0xffffffff {
			return fmt.Errorf("--overlay requires a valid --host-pid")
		}
		return win.RunOverlay(uint32(o.pid), o.configPath, version)
	}
	return win.RunWatcher(o.configPath, !o.watcher)
}

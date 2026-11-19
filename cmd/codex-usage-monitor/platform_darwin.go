package main

import (
	"fmt"
	"os"
	"runtime"

	"codex-usage-monitor/internal/mac"
)

// Cocoa's event loop must run on the process's original main OS thread.
func init()             { runtime.LockOSThread() }
func prepareConsole()   {}
func showError(e error) { fmt.Fprintln(os.Stderr, e); mac.ShowError(e.Error()) }
func platformRun(o options) error {
	if o.addStartMenu || o.removeStartMenu {
		return fmt.Errorf("Start menu shortcuts apply only to Windows")
	}
	if o.install && o.uninstall {
		return fmt.Errorf("choose install or uninstall")
	}
	if o.install || o.uninstall {
		return mac.Startup(o.configPath, o.install)
	}
	if o.startupStatus {
		on, e := mac.StartupStatus(o.configPath)
		if e != nil {
			return e
		}
		fmt.Printf("Start at login: %t\n", on)
		return nil
	}
	if o.stop {
		return mac.Stop(o.configPath)
	}
	if o.diagnostics {
		b, e := mac.Diagnostics(o.configPath)
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
	}
	if o.overlay {
		if o.pid == 0 || uint64(o.pid) > 0x7fffffff {
			return fmt.Errorf("--overlay requires a valid --host-pid")
		}
		return mac.RunOverlay(uint32(o.pid), o.configPath, version)
	}
	return mac.RunWatcher(o.configPath, !o.watcher)
}

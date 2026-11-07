package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/rollout"
)

var version = "dev"

type options struct {
	watcher, overlay, install, uninstall, startupStatus, stop, diagnostics bool
	addStartMenu, removeStartMenu                                          bool
	pid                                                                    uint
	configPath, inspect                                                    string
}

func main() {
	if e := run(); e != nil {
		showError(e)
		os.Exit(1)
	}
}

func run() error {
	var o options
	fs := flag.NewFlagSet("CodexUsageMonitor", flag.ContinueOnError)
	fs.BoolVar(&o.watcher, "watcher", false, "run the lifecycle watcher (default)")
	fs.BoolVar(&o.overlay, "overlay", false, "run an overlay bound to --host-pid")
	fs.UintVar(&o.pid, "host-pid", 0, "Codex Desktop main-process PID")
	fs.StringVar(&o.configPath, "config", config.DefaultPath(), "settings path")
	fs.StringVar(&o.inspect, "inspect", "", "read a rollout file and print accounting JSON only")
	fs.BoolVar(&o.install, "install-startup", false, "register current-user Windows logon startup")
	fs.BoolVar(&o.uninstall, "uninstall-startup", false, "remove this application's startup value")
	fs.BoolVar(&o.startupStatus, "startup-status", false, "print startup registration status")
	fs.BoolVar(&o.addStartMenu, "add-start-menu", false, "create or repair the current-user Start menu shortcut")
	fs.BoolVar(&o.removeStartMenu, "remove-start-menu", false, "remove the current-user Start menu shortcut")
	fs.BoolVar(&o.stop, "stop", false, "stop the watcher and its overlays")
	fs.BoolVar(&o.diagnostics, "diagnostics", false, "print running monitor process resource counters")
	showVersion := fs.Bool("version", false, "print version")
	if e := fs.Parse(os.Args[1:]); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if o.inspect != "" || *showVersion || o.install || o.uninstall || o.startupStatus || o.stop || o.diagnostics || o.addStartMenu || o.removeStartMenu {
		prepareConsole()
	}
	if *showVersion {
		fmt.Println("Codex Usage Monitor", version)
		return nil
	}
	if o.inspect != "" {
		r := rollout.New(o.inspect)
		defer r.Close()
		if _, e := r.Update(context.Background()); e != nil {
			return e
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r.State.Snapshot())
	}
	var e error
	o.configPath, e = filepath.Abs(o.configPath)
	if e != nil {
		return e
	}
	return platformRun(o)
}

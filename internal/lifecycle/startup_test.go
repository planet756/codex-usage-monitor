package lifecycle

import (
	"errors"
	"path/filepath"
	"testing"

	"codex-usage-monitor/internal/config"
)

func TestStartupDefaultRegistersOnceAndOptOutSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	registered, probes, writes := false, 0, 0
	launch := func() error {
		cfg, err := config.Load(p)
		if err != nil {
			return err
		}
		return EnsureStartup(cfg.StartWithWindows, func() (bool, error) {
			probes++
			return registered, nil
		}, func() error {
			writes++
			registered = true
			return nil
		})
	}
	for range 3 {
		if err := launch(); err != nil {
			t.Fatal(err)
		}
	}
	if !registered || probes != 3 || writes != 1 {
		t.Fatalf("default or unchanged-launch behavior failed: registered=%v probes=%d writes=%d", registered, probes, writes)
	}
	if err := config.UpdateStartup(p, false, func(v bool) error { registered = v; return nil }); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := launch(); err != nil {
			t.Fatal(err)
		}
	}
	if registered || probes != 3 || writes != 1 {
		t.Fatal("opt-out was probed or re-enabled after restart")
	}
	if err := config.UpdateStartup(p, true, func(v bool) error { registered = v; return nil }); err != nil {
		t.Fatal(err)
	}
	registered = false // The portable EXE moved, so the old command no longer matches.
	if err := launch(); err != nil || !registered || writes != 2 {
		t.Fatalf("enabled registration was not repaired: %v", err)
	}
}

func TestStartupErrorsRemainVisibleAndNeverRegisterAfterReadFailure(t *testing.T) {
	denied := errors.New("access denied")
	called := false
	if err := EnsureStartup(true, func() (bool, error) { return false, denied }, func() error { called = true; return nil }); !errors.Is(err, denied) || called {
		t.Fatal("registry read failure was ignored")
	}
	if err := EnsureStartup(true, func() (bool, error) { return false, nil }, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal("registration failure was swallowed")
	}
	if err := EnsureStartup(false, func() (bool, error) { t.Fatal("disabled startup was probed"); return false, nil }, func() error { t.Fatal("disabled startup was registered"); return nil }); err != nil {
		t.Fatal(err)
	}
}

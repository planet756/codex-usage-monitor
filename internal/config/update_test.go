package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestImmediateUIChangesPersistAndPreserveLatestDataSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	for _, lang := range []string{"zh-CN", "en"} {
		latest := Config{CodexHome: "latest-data-source", Language: "en", Theme: "auto"}
		if err := Save(p, latest); err != nil {
			t.Fatal(err)
		}
		applied, err := UpdateUI(p, lang, "dark")
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(p)
		if err != nil || loaded != applied || loaded.Language != lang || loaded.Theme != "dark" || loaded.CodexHome != latest.CodexHome || loaded.StartWithWindows != latest.StartWithWindows {
			t.Fatalf("immediate change lost settings: %+v %v", loaded, err)
		}
		if _, err := UpdateUI(p, "invalid", "light"); err == nil {
			t.Fatal("invalid selection saved")
		}
		after, _ := Load(p)
		if after != loaded {
			t.Fatal("failed change modified saved settings")
		}
	}
}

func TestSettingsSchemaContainsOnlyCurrentOptions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	for _, contents := range []string{`{"wsl_distribution":"obsolete"}`, `{"wsl_mount_root":"/obsolete"}`} {
		if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatal("removed option was silently accepted")
		}
	}
	cfg := Default()
	cfg.CodexHome = `D:\AI Data`
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(p); err != nil || loaded != cfg {
		t.Fatal(loaded, err)
	}
}

func TestStartupTogglePersistsBeforeRegistrationAndKeepsUISettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	cfg := Default()
	cfg.Language, cfg.Theme, cfg.CodexHome = "zh-CN", "dark", "custom-data-source"
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, false, true, true, false} {
		calls := 0
		if err := UpdateStartup(p, enabled, func(value bool) error {
			calls++
			stored, err := Load(p)
			if err != nil || value != enabled || stored.StartWithWindows != enabled || stored.Language != cfg.Language || stored.Theme != cfg.Theme || stored.CodexHome != cfg.CodexHome {
				t.Fatalf("registration started with inconsistent preference: %+v %v", stored, err)
			}
			return nil
		}); err != nil || calls != 1 {
			t.Fatalf("registration not applied: %d %v", calls, err)
		}
		reloaded, err := Load(p)
		if err != nil || reloaded.StartWithWindows != enabled {
			t.Fatalf("toggle lost after relaunch: %+v %v", reloaded, err)
		}
	}
}

func TestStartupRegistrationFailureRestoresSavedPreference(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	registrationErr := errors.New("registration denied")
	for _, enabled := range []bool{false, true} {
		before := Default()
		before.StartWithWindows = !enabled
		if err := Save(p, before); err != nil {
			t.Fatal(err)
		}
		err := UpdateStartup(p, enabled, func(bool) error { return registrationErr })
		after, readErr := Load(p)
		if !errors.Is(err, registrationErr) || readErr != nil || after != before {
			t.Fatalf("failed change was retained: %+v %v %v", after, err, readErr)
		}
	}
}

func TestInvalidStartupSettingsNeverTouchRegistration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	for _, contents := range []string{`{`, `{"start_with_windows":"yes"}`, `{"language":"unknown"}`} {
		if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		err := UpdateStartup(p, false, func(bool) error { called = true; return nil })
		after, _ := os.ReadFile(p)
		if err == nil || called || string(after) != contents {
			t.Fatal("invalid settings changed registration or were overwritten")
		}
	}
}

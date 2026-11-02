package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupDefaultsOnAndExplicitOptOutSurvivesReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	for _, contents := range []string{"", `{}`, `{"language":"zh-CN","theme":"dark"}`} {
		if contents != "" {
			if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := Load(p)
		if err != nil || !cfg.StartWithWindows {
			t.Fatalf("startup was not enabled by default: %+v %v", cfg, err)
		}
	}
	cfg := Default()
	cfg.StartWithWindows = false
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err = json.Unmarshal(b, &stored); err != nil || string(stored["start_with_windows"]) != "false" {
		t.Fatal("false preference omitted from settings", string(b), err)
	}
	reloaded, err := Load(p)
	if err != nil || reloaded.StartWithWindows {
		t.Fatalf("explicit opt-out reset on reload: %+v %v", reloaded, err)
	}
}

func TestSettingsRoundTripAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "settings.json")
	c := Default()
	c.Language = "zh-CN"
	c.Theme = "dark"
	if e := Save(p, c); e != nil {
		t.Fatal(e)
	}
	loaded, e := Load(p)
	if e != nil || loaded != c {
		t.Fatalf("settings round trip: %+v %v", loaded, e)
	}
	_ = os.WriteFile(p, []byte(`{"language":"unknown"}`), 0600)
	if _, e = Load(p); e == nil {
		t.Fatal("accepted invalid language")
	}
	_ = os.WriteFile(p, []byte(`{`), 0600)
	if _, e = Load(p); e == nil {
		t.Fatal("silently overwrote invalid settings")
	}
}

func TestUnknownSettingsAreRejectedWithoutMigration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"language":"en","unknown_option":true}`)
	if err := os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown setting accepted")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(original) {
		t.Fatal("invalid settings were rewritten")
	}
}

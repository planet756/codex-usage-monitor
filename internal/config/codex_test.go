package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStateDatabaseUsesCodexConfigurationPrecedence(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.toml")
	env := t.TempDir()
	t.Setenv("CODEX_SQLITE_HOME", "")
	check := func(dir string) {
		t.Helper()
		want := filepath.Join(dir, "state_5.sqlite")
		if got, err := StateDatabase(home); err != nil || got != want {
			t.Fatalf("state database = %q, %v; want %q", got, err, want)
		}
	}
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(config, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check(home)
	t.Setenv("CODEX_SQLITE_HOME", env)
	check(env)
	write("[profiles.example]\nsqlite_home = 'unselected'\n")
	check(env)
	write("sqlite_home = 'relative folder'\n")
	check(filepath.Join(home, "relative folder"))
	explicit := t.TempDir()
	write(fmt.Sprintf("sqlite_home = '%s'\n", explicit))
	check(explicit)
	write("sqlite_home = true\n")
	if _, err := StateDatabase(home); err == nil {
		t.Fatal("invalid config silently selected the environment directory")
	}
	write("")
	t.Setenv("CODEX_SQLITE_HOME", "relative")
	if _, err := StateDatabase(home); err == nil {
		t.Fatal("relative environment override was resolved against the monitor's cwd")
	}
}

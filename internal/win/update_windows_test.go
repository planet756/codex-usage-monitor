package win

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStagePortableExecutable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entries   []string
		wantError bool
	}{
		{"root executable", []string{"LICENSE", monitorEXE, "config.toml"}, false},
		{"missing root executable", []string{"nested/" + monitorEXE}, true},
		{"duplicate executable", []string{monitorEXE, monitorEXE}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "portable.zip"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			archive := zip.NewWriter(file)
			for _, name := range tc.entries {
				entry, err := archive.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(entry, name); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			reader, err := os.Open(file.Name())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			dir := t.TempDir()
			staged, err := stagePortableExecutable(reader, dir)
			if (err != nil) != tc.wantError {
				t.Fatalf("stagePortableExecutable() path, err = %q, %v", staged, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError {
				if len(entries) != 0 {
					t.Fatalf("invalid archive wrote files: %v", entries)
				}
				return
			}
			if len(entries) != 1 || filepath.Join(dir, entries[0].Name()) != staged {
				t.Fatalf("only the staged executable should be extracted: %v", entries)
			}
			data, err := os.ReadFile(staged)
			if err != nil || string(data) != monitorEXE {
				t.Fatalf("staged executable = %q, %v", data, err)
			}
			target := filepath.Join(dir, monitorEXE)
			settings := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(target, []byte("old executable"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte("user settings"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := replacePortableExecutable(staged, target); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(target)
			if err != nil || string(data) != monitorEXE {
				t.Fatalf("replacement executable = %q, %v", data, err)
			}
			data, err = os.ReadFile(settings)
			if err != nil || string(data) != "user settings" {
				t.Fatalf("preserved settings = %q, %v", data, err)
			}
		})
	}
}

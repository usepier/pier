package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRepoAutoCompose(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "missing config defaults on", want: true},
		{name: "empty config defaults on", body: "# repository settings\n", want: true},
		{name: "explicit on", body: "[docker]\nauto_up = true\n", want: true},
		{name: "explicit opt out", body: "[docker]\nauto_up = false\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.body != "" {
				if err := os.WriteFile(filepath.Join(root, ".pier.toml"), []byte(tt.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadRepo(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.AutoCompose(); got != tt.want {
				t.Errorf("AutoCompose() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadRepoRejectsInvalidTOML(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".pier.toml")
	if err := os.WriteFile(path, []byte("[docker\nauto_up = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRepo(root)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("LoadRepo() error = %v, want error naming %s", err, path)
	}
}

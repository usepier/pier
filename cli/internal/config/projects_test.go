package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberProjectDeduplicatesPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "Documents", "pier")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RememberProject(repo); err != nil {
		t.Fatal(err)
	}
	if err := RememberProject(repo); err != nil {
		t.Fatal(err)
	}
	paths := ProjectPaths()
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != resolved {
		t.Fatalf("ProjectPaths() = %v, want [%s]", paths, resolved)
	}
}

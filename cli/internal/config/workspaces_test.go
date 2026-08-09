package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberAndForgetWorkspace(t *testing.T) {
	previousHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { _ = os.Setenv("HOME", previousHome) })

	repo := filepath.Join(os.Getenv("HOME"), "Documents", "pier")
	if err := RememberWorkspace("i-123", repo); err != nil {
		t.Fatal(err)
	}
	if got := WorkspacePaths()["i-123"]; got != repo {
		t.Errorf("workspace = %q, want %q", got, repo)
	}
	if err := ForgetWorkspace("i-123"); err != nil {
		t.Fatal(err)
	}
	if _, ok := WorkspacePaths()["i-123"]; ok {
		t.Error("forgotten workspace remains in registry")
	}
}

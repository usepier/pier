package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBakeHookUsesPierDirectory(t *testing.T) {
	root := t.TempDir()
	if got := BakeHook(root); got != "" {
		t.Fatalf("BakeHook() without config = %q", got)
	}
	configDir := filepath.Join(root, ".pier")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(configDir, "bake.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := BakeHook(root); got != hook {
		t.Fatalf("BakeHook() = %q, want %q", got, hook)
	}
}

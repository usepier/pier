package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func checkout(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "shop")
	os.MkdirAll(dir, 0o755)
	return dir
}

func write(t *testing.T, path, data, base string) (map[string]string, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"repo": "shop", "path": path, "data": base64.StdEncoding.EncodeToString([]byte(data)), "base_sha": base})
	v, err := writeOp(raw)
	if err != nil {
		return nil, err
	}
	return v.(map[string]string), nil
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestWriteOp(t *testing.T) {
	dir := checkout(t)
	// A new file: no base.
	r, err := write(t, "src/a.txt", "one\n", "")
	if err != nil || r["sha"] != sha("one\n") {
		t.Fatalf("new file: %v %v", r, err)
	}
	// Saved over what was read.
	if _, err := write(t, "src/a.txt", "two\n", sha("one\n")); err != nil {
		t.Fatal(err)
	}
	// Changed underneath: refused.
	if _, err := write(t, "src/a.txt", "three\n", sha("one\n")); err == nil || !strings.Contains(err.Error(), "conflict:") {
		t.Errorf("stale write = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "src/a.txt")); string(b) != "two\n" {
		t.Errorf("file = %q", b)
	}
	// Never outside the checkout.
	os.Symlink("/etc", filepath.Join(dir, "etc"))
	for _, p := range []string{"../escape", "/etc/passwd", "etc/passwd", "etc"} {
		if _, err := write(t, p, "x", ""); err == nil {
			t.Errorf("%s was written", p)
		}
	}
	if _, err := writeOp(json.RawMessage(`{"repo":"../x","path":"a"}`)); err == nil {
		t.Error("a bad repo name was accepted")
	}
}

func TestGitCommitOp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := checkout(t)
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	v, err := gitOp(json.RawMessage(`{"repo":"shop","action":"commit","message":"add x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := v.(map[string]string)["head"]; len(h) != 40 {
		t.Errorf("head = %q", h)
	}
	if _, err := gitOp(json.RawMessage(`{"repo":"shop","action":"commit","message":"again"}`)); err == nil {
		t.Error("an empty commit succeeded")
	}
}

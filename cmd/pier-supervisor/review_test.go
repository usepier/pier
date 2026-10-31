package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// checkout makes a session's checkout: ~/work/shop with one commit, its
// base.
func checkout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "shop")
	os.MkdirAll(dir, 0o755)
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "a@b")
	gitIn(t, dir, "config", "user.name", "a")
	os.WriteFile(filepath.Join(dir, "README"), []byte("shop\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "start")
	gitIn(t, dir, "update-ref", "refs/pier/base", "HEAD")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return string(out)
}

func write(t *testing.T, path, data, base string) (map[string]string, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"path": path, "data": base64.StdEncoding.EncodeToString([]byte(data)), "base_sha": base})
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
	if _, err := write(t, "src/a.txt", "three\n", sha("one\n")); errCode(err) != codeConflict {
		t.Errorf("stale write = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "src/a.txt")); string(b) != "two\n" {
		t.Errorf("file = %q", b)
	}
	// Never outside the checkout.
	os.Symlink("/etc", filepath.Join(dir, "etc"))
	for _, p := range []string{"../escape", "/etc/passwd", "etc/passwd", "etc"} {
		if _, err := write(t, p, "x", ""); errCode(err) != codeOutside {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestOpsWithoutACheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := write(t, "a", "x", ""); errCode(err) != codeNoCheckout {
		t.Errorf("write = %v", err)
	}
	if _, err := queries["repo.changes"](nil); errCode(err) != codeNoCheckout {
		t.Errorf("changes = %v", err)
	}
}

func TestGitCommitOp(t *testing.T) {
	dir := checkout(t)
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	v, err := gitOp(json.RawMessage(`{"action":"commit","message":"add x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := v.(map[string]string)["head"]; len(h) != 40 {
		t.Errorf("head = %q", h)
	}
	if _, err := gitOp(json.RawMessage(`{"action":"commit","message":"again"}`)); errCode(err) != codeGit {
		t.Errorf("an empty commit: %v", err)
	}
}

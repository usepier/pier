package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchSlug(t *testing.T) {
	for in, want := range map[string]string{
		"fix-login-redirect":                         "fix-login-redirect",
		"Sure! Here's a name:\n`Fix Login Redirect`": "fix-login-redirect",
		"feat/Add dark mode":                         "feat/add-dark-mode",
		"  ...  ":                                    "",
		strings.Repeat("word-", 20):                  "word-word-word-word-word-word-word-word-word",
	} {
		if got := branchSlug(in); got != want {
			t.Errorf("branchSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// git runs in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func branchCheckout(t *testing.T, branch string) string {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", branch)
	git(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	return dir
}

// The stand-in branch takes the name, or the next free one; a branch that
// was pushed, or a checkout that moved to another branch, keeps its name.
func TestRenameBranch(t *testing.T) {
	dir := branchCheckout(t, "oslo")
	git(t, dir, "branch", "fix-login")
	got, err := renameBranch(dir, "oslo", "fix-login")
	if err != nil || got != "fix-login-2" || git(t, dir, "symbolic-ref", "--short", "HEAD") != "fix-login-2" {
		t.Errorf("rename = %q, %v; on %s", got, err, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	}

	moved := branchCheckout(t, "oslo")
	git(t, moved, "checkout", "-q", "-b", "agents-own")
	if _, err := renameBranch(moved, "oslo", "fix-login"); err == nil {
		t.Error("renamed a checkout that moved to another branch")
	}

	pushed := branchCheckout(t, "oslo")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	git(t, pushed, "remote", "add", "origin", remote)
	git(t, pushed, "push", "-q", "-u", "origin", "oslo")
	if _, err := renameBranch(pushed, "oslo", "fix-login"); err == nil {
		t.Error("renamed a branch that was pushed")
	}
}

// The status names the branch the session named itself while the checkout
// is on it, and the checkout's own branch once it's pushed under its name
// — never the branch the session came off.
func TestSessionBranches(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "work", "shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "-u", "origin", "main")
	git(t, dir, "config", "pier.base", "main")
	if r, o := sessionBranches(home); r != "" || o != "" {
		t.Errorf("on the base: %q %q", r, o)
	}

	git(t, dir, "checkout", "-q", "-b", "fix-login")
	b, _ := json.Marshal(branchName{From: "oslo", To: "fix-login"})
	if err := os.MkdirAll(filepath.Join(home, ".pier"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(branchNamePath(home), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, o := sessionBranches(home); r != "fix-login" || o != "" {
		t.Errorf("named, not pushed: %q %q", r, o)
	}

	git(t, dir, "checkout", "-q", "-b", "kur-12-fix-login")
	if r, o := sessionBranches(home); r != "" || o != "" {
		t.Errorf("the agent's own, not pushed: %q %q", r, o)
	}
	git(t, dir, "push", "-q", "-u", "origin", "kur-12-fix-login")
	if r, o := sessionBranches(home); r != "" || o != "kur-12-fix-login" {
		t.Errorf("the agent's own, pushed: %q %q", r, o)
	}
}

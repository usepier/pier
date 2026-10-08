package pier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/pkg/pier/gitview"
)

// execDriver runs a session's one-off commands here, with HOME standing in
// for the session's home.
type execDriver struct {
	driver.Driver
	home string
}

func (d execDriver) Exec(ctx context.Context, _ string, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+d.home)
	out, err := cmd.Output()
	return string(out), err
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

func TestReviewScripts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	dir := filepath.Join(home, "work", "shop")
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("same\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "update-ref", "refs/pier/base", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "fix")
	git(t, dir, "mv", "old.txt", "new name.txt")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "work")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n2\nthree hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x\ny\n"), 0o644)

	c := &Client{drv: execDriver{home: home}}
	s := Session{ID: "i-1", Name: "fix", Repo: "shop", State: StateRunning}
	ctx := context.Background()
	ch, err := c.Changes(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]gitview.File{}
	for _, f := range ch.Files {
		got[f.Path] = f
	}
	if f := got["a.txt"]; f.Status != gitview.Modified || f.Added != 2 || f.Removed != 1 || !f.Uncommitted {
		t.Errorf("a.txt = %+v", f)
	}
	if f := got["b.txt"]; f.Status != gitview.Added || f.Uncommitted {
		t.Errorf("b.txt = %+v", f)
	}
	if f := got["new name.txt"]; f.Status != gitview.Renamed || f.OldPath != "old.txt" {
		t.Errorf("rename = %+v", f)
	}
	if f := got["notes.md"]; f.Status != gitview.Untracked || f.Added != 2 {
		t.Errorf("untracked = %+v", f)
	}
	if ch.Branch.Head != "fix" || ch.BaseOid == "" {
		t.Errorf("branch = %+v base %q", ch.Branch, ch.BaseOid)
	}
	if cached, _, ok := CachedChanges("i-1"); !ok || len(cached.Files) != len(ch.Files) {
		t.Error("the snapshot wasn't kept for when the session parks")
	}

	patch, _, err := c.Diff(ctx, s, got["a.txt"], 0)
	if err != nil || !strings.Contains(patch, "+three hello") || !strings.Contains(patch, "-two") {
		t.Errorf("diff = %q %v", patch, err)
	}
	patch, _, _ = c.Diff(ctx, s, got["notes.md"], 0)
	if !strings.Contains(patch, "+y") {
		t.Errorf("untracked diff = %q", patch)
	}
	if p, _, ok := CachedDiff("i-1", "notes.md"); !ok || p != patch {
		t.Error("the diff wasn't kept")
	}

	fc, err := c.ReadFile(ctx, s, "a.txt")
	if err != nil || string(fc.Data) != "one\n2\nthree hello\n" || fc.Size != 18 || len(fc.Sha) != 64 {
		t.Errorf("read = %+v %v", fc, err)
	}
	if fc, _ := c.ReadFile(ctx, s, "nope.txt"); !fc.Missing {
		t.Error("a missing file reads as missing")
	}
	if _, err := c.ReadFile(ctx, s, "../x"); err == nil {
		t.Error("read outside the checkout")
	}

	ms, _, err := c.Search(ctx, s, "hello", false, false)
	if err != nil || len(ms) != 2 {
		t.Fatalf("search = %+v %v", ms, err)
	}
	if ms[0].Path != "a.txt" || ms[0].Line != 3 || ms[0].Col != 7 || ms[1].Path != "b.txt" {
		t.Errorf("matches = %+v", ms)
	}
}

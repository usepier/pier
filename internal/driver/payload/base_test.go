package payload

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A clone that never pulled still gets what origin's default branch has:
// it's what sessions, the pool and images are made from.
func TestOriginBaseIsWhatOriginHas(t *testing.T) {
	origin := t.TempDir()
	runGit(t, origin, "init", "-q", "-b", "main")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "first")
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(clone), "clone", "-q", origin, clone)

	// Origin moves on: the repo gets set up for pier there.
	os.MkdirAll(filepath.Join(origin, ".pier"), 0o755)
	os.WriteFile(filepath.Join(origin, ".pier", "setup.sh"), []byte("make dev\n"), 0o755)
	os.WriteFile(filepath.Join(origin, ".pier", "bake.sh"), []byte("install go\n"), 0o755)
	runGit(t, origin, "add", ".pier")
	runGit(t, origin, "commit", "-q", "-m", "set up for pier")

	if KnownBase(clone) != "origin/main" || HasDir(clone, KnownBase(clone), ".pier") {
		t.Fatal("before a fetch the clone can't know")
	}
	base := OriginBase(context.Background(), clone)
	if base != "origin/main" {
		t.Fatalf("base = %q", base)
	}
	if !HasDir(clone, base, ".pier") || string(FileAt(clone, base, ".pier/setup.sh")) != "make dev\n" || string(BakeHook(clone, base)) != "install go\n" {
		t.Error("origin's .pier/ unread")
	}
	// The clone itself was left as it was.
	if _, err := os.Stat(filepath.Join(clone, ".pier")); err == nil {
		t.Error("the clone's working tree changed")
	}
	if FileAt(clone, base, ".pier/none.sh") != nil || HasDir(clone, base, "nope") {
		t.Error("absent files read as present")
	}
}

// A repo without an origin is its clone as it is.
func TestBaseWithoutOrigin(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	os.MkdirAll(filepath.Join(repo, ".pier"), 0o755)
	os.WriteFile(filepath.Join(repo, ".pier", "setup.sh"), []byte("uncommitted\n"), 0o755)
	if base := OriginBase(context.Background(), repo); base != "HEAD" {
		t.Fatalf("base = %q", base)
	}
	if string(FileAt(repo, "HEAD", ".pier/setup.sh")) != "uncommitted\n" || !HasDir(repo, "HEAD", ".pier") {
		t.Error("the clone as it is unread")
	}
}

// A session from origin's branch is that branch, even when the clone sits
// on its commit with edits on top; asked for HEAD, the edits ride along.
func TestLocalEditsRideOnlyALocalBase(t *testing.T) {
	origin := t.TempDir()
	runGit(t, origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\n"), 0o644)
	runGit(t, origin, "add", "a.txt")
	runGit(t, origin, "commit", "-q", "-m", "first")
	clone := filepath.Join(t.TempDir(), "shop")
	runGit(t, filepath.Dir(clone), "clone", "-q", origin, clone)
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("edited here\n"), 0o644)

	patched := func(base string) bool {
		pl, err := Build(context.Background(), t.TempDir(), driver.CreateSpec{Name: "fix", Repo: clone, Branch: "fix", BaseRef: base}, []byte("sup"), nil, nil, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pl.Pushes {
			if filepath.Base(p.Local) == "pier-dirty.patch" {
				return true
			}
		}
		return false
	}
	if patched("origin/main") {
		t.Error("the clone's edits rode a session from origin/main")
	}
	if !patched("HEAD") {
		t.Error("the clone's edits didn't ride a session from HEAD")
	}
}

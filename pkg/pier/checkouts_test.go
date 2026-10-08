package pier

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/usepier/pier/internal/config"
)

// A linked checkout survives in the config, reports its origin, and flags
// itself once the clone is gone.
func TestLinkCheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:o/app.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sub := filepath.Join(repo, "src")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: config.Default()}
	co, err := c.LinkCheckout(sub) // any directory inside links the repo root
	if err != nil {
		t.Fatal(err)
	}
	if co.Origin != "github.com/o/app" || filepath.Base(co.Path) != filepath.Base(repo) {
		t.Fatalf("LinkCheckout = %+v", co)
	}
	saved, err := config.Load()
	if err != nil || saved.Checkouts[co.Path] != "github.com/o/app" {
		t.Fatalf("the link must persist: %v %v", saved.Checkouts, err)
	}
	if got := c.Checkouts(); len(got) != 1 || got[0].Missing || CheckoutKey(got[0]) != "github.com/o/app" {
		t.Fatalf("Checkouts = %+v", got)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if got := c.Checkouts(); len(got) != 1 || !got[0].Missing {
		t.Fatalf("a deleted clone must read missing: %+v", got)
	}
	if err := c.UnlinkCheckout(co.Path); err != nil || len(c.Checkouts()) != 0 {
		t.Fatalf("unlink: %v %+v", err, c.Checkouts())
	}
	if _, err := c.LinkCheckout(t.TempDir()); err == nil {
		t.Error("a directory outside any repository must not link")
	}
}

func TestRepoKey(t *testing.T) {
	if got := RepoKey(Session{Repo: "app", Origin: "github.com/o/app"}); got != "github.com/o/app" {
		t.Errorf("RepoKey with origin = %q", got)
	}
	if got := RepoKey(Session{Repo: "app"}); got != "name:app" {
		t.Errorf("RepoKey without origin = %q", got)
	}
}

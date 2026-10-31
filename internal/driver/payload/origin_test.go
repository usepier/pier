package payload

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
)

// Every spelling of one remote reduces to the same key, and credentials in
// the URL never reach it.
func TestCanonicalOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"git@github.com:UsePier/Pier.git":                  "github.com/usepier/pier",
		"ssh://git@github.com/usepier/pier.git":            "github.com/usepier/pier",
		"https://github.com/usepier/pier":                  "github.com/usepier/pier",
		"https://github.com/usepier/pier.git/":             "github.com/usepier/pier",
		"https://x-access-token:ghp_secret@github.com/o/r": "github.com/o/r",
		"https://gitlab.example.com:8443/group/sub/repo":   "gitlab.example.com/group/sub/repo",
		"  git@git.internal:team/app  ":                    "git.internal/team/app",
		"/srv/git/repo.git":                                "",
		"file:///srv/git/repo.git":                         "",
		"../relative/repo":                                 "",
		"":                                                 "",
		"https://github.com/o/r%20space":                   "",
	} {
		if got := CanonicalOrigin(raw); got != want {
			t.Errorf("CanonicalOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The base is the origin branch a session builds on: an explicit base origin
// has, else HEAD's branch when origin has it, else origin's default branch.
func TestBaseBranch(t *testing.T) {
	remote, root := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(remote, "init", "-q", "--bare", "-b", "trunk")
	git(root, "init", "-q", "-b", "trunk")
	git(root, "commit", "-q", "--allow-empty", "-m", "init")
	git(root, "remote", "add", "origin", remote)
	git(root, "push", "-q", "origin", "trunk")
	git(root, "checkout", "-q", "-b", "shared")
	git(root, "push", "-q", "origin", "shared")
	git(root, "checkout", "-q", "-b", "local-only")
	git(root, "fetch", "-q", "origin")
	git(root, "remote", "set-head", "origin", "trunk")

	for _, c := range []struct{ ref, want string }{
		{"HEAD", "trunk"}, // local-only is checked out: origin's default
		{"shared", "shared"},
		{"origin/shared", "shared"},
		{"local-only", "trunk"},
		{"no-such-branch", "trunk"},
	} {
		if got := BaseBranch(root, c.ref); got != c.want {
			t.Errorf("BaseBranch(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
	git(root, "checkout", "-q", "shared")
	if got := BaseBranch(root, ""); got != "shared" {
		t.Errorf("a checked-out branch origin has is the base, got %q", got)
	}
	if got := BaseBranch(t.TempDir(), "HEAD"); got != "" {
		t.Errorf("no repository, no base; got %q", got)
	}
}

// The checkout records its starting point for in-VM tooling, on create and
// on claim alike.
func TestScriptsRecordTheBase(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: t.TempDir(), Branch: "feat"}
	boot := renderBootstrap(spec, "origin", "abc123", "", "")
	if !strings.Contains(boot, "git update-ref refs/pier/base abc123") || !strings.Contains(boot, "git config --unset pier.base") {
		t.Errorf("bootstrap must record the base commit and branch:\n%s", boot)
	}
	reset := strings.Index(boot, "git reset -q --hard abc123")
	if mark := strings.Index(boot, "refs/pier/base"); mark < reset {
		t.Error("the base is recorded after the checkout lands on it")
	}
	if !strings.Contains(freshenTmpl, recordBase) {
		t.Error("a claim must re-record the base: the fill's is not the session's")
	}
}

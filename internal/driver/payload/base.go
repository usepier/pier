package payload

// What a repo's sessions, pool and image are made from: origin's default
// branch, as origin has it — not the clone on this Mac, which can be behind
// it (never pulled) or carry work that isn't there. A session can still be
// given a base of its own (HEAD: the clone as it is). A repo with no origin
// has only its clone: HEAD.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// OriginBase brings origin's default branch up to date and returns it as a
// ref ("origin/main"); HEAD when the repo has none.
func OriginBase(ctx context.Context, repo string) string {
	if b := DefaultBranch(repo); b != "" {
		fetchBase(ctx, repo, "origin/"+b)
	}
	return KnownBase(repo)
}

// KnownBase is OriginBase as this clone last fetched it, without fetching:
// for what's read often (a listing), and kept fresh by OriginBase.
func KnownBase(repo string) string {
	if b := DefaultBranch(repo); b != "" {
		return "origin/" + b
	}
	return "HEAD"
}

// FileAt is a file of the repo (rel, slash-separated) as base has it
// committed; HEAD is the clone as it is, working tree included. nil when
// base has no such file.
func FileAt(repo, base, rel string) []byte {
	if base == "" || base == "HEAD" {
		b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			return nil
		}
		return b
	}
	b, err := exec.Command("git", "-C", repo, "show", base+":"+rel).Output()
	if err != nil {
		return nil
	}
	return b
}

// HasDir reports whether base has the directory rel.
func HasDir(repo, base, rel string) bool {
	if base == "" || base == "HEAD" {
		fi, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
		return err == nil && fi.IsDir()
	}
	t, err := gitOut(repo, "cat-file", "-t", base+":"+rel)
	return err == nil && t == "tree"
}

// BakeHook is the repo's .pier/bake.sh as base has it; nil when absent. It
// runs on the bake instance (agent user, passwordless sudo, no checkout yet):
// toolchains belong there, repo state in .pier/setup.sh.
func BakeHook(repo, base string) []byte {
	return FileAt(repo, base, ".pier/bake.sh")
}

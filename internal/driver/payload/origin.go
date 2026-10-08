package payload

import (
	"net/url"
	"strings"
)

// RepoOrigin is the repo's origin remote in canonical form (CanonicalOrigin),
// or "" when it has none. Sessions carry it so frontends can group sessions
// from different checkouts of one repository, and find its pull requests.
func RepoOrigin(repo string) string {
	raw, err := gitOut(repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return CanonicalOrigin(raw)
}

// CanonicalOrigin reduces a git remote URL to host/path, lowercased: one
// string for every way of writing the same remote (ssh, scp-like, https,
// with or without .git). Credentials in the URL never survive — the result
// lands in instance tags, readable by anyone who can list instances. "" when
// the URL has no host (a local path) or holds characters a tag can't carry.
func CanonicalOrigin(raw string) string {
	s := strings.TrimSpace(raw)
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	default:
		// scp-like: [user@]host:path. A local path has no colon before its
		// first slash.
		hp, p, ok := strings.Cut(s, ":")
		if !ok || strings.Contains(hp, "/") {
			return ""
		}
		if _, h, found := strings.Cut(hp, "@"); found {
			hp = h
		}
		host, path = hp, p
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	out := strings.ToLower(host + "/" + path)
	if len(out) > 200 {
		return ""
	}
	for _, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', strings.ContainsRune("./-_~", r):
		default:
			return ""
		}
	}
	return out
}

// BaseBranch names the origin branch a session created from baseRef builds
// on — what its pull request targets and its changes are compared against.
// An explicit base that origin has wins (a local name or origin/<name>);
// with the default HEAD base, the checked-out branch when origin has it.
// Anything else falls back to origin's default branch. "" when origin has
// none of these: no origin, or one this checkout never fetched.
func BaseBranch(repo, baseRef string) string {
	usable := func(b string) bool {
		if b == "" || checkName(b) != nil {
			return false
		}
		_, err := gitOut(repo, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+b)
		return err == nil
	}
	if baseRef == "" || baseRef == "HEAD" {
		if b, err := gitOut(repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && usable(b) {
			return b
		}
	} else if b := strings.TrimPrefix(strings.TrimPrefix(baseRef, "refs/remotes/"), "origin/"); usable(b) {
		return b
	}
	if d, err := gitOut(repo, "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(d, "origin/"); usable(b) {
			return b
		}
	}
	for _, b := range []string{"main", "master"} {
		if usable(b) {
			return b
		}
	}
	return ""
}

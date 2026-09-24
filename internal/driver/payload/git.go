package payload

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func gitOut(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// originInfo decides how the repo reaches the VM. mode "origin": the base
// commit is reachable from an origin ref, so the VM fetches it from GitHub
// and nothing repo-shaped rides the tunnel. "thin": GitHub has the shared
// history, ship only the local-delta bundle. "full": no usable GitHub origin
// — ship everything. The returned url is what the VM's origin remote is set
// to (GitHub ssh forms become https so the GH_TOKEN credential helper can
// auth; when only the laptop's ssh agent can auth, Build swaps the ssh URL
// back in and flags the agent forward — ~/.ssh never travels either way).
func originInfo(repo, sha string) (mode, url string) {
	raw, err := gitOut(repo, "remote", "get-url", "origin")
	if err != nil || raw == "" {
		return "full", ""
	}
	fetchable := fetchURL(raw)
	if fetchable == "" {
		return "full", raw // non-GitHub origin: still add the remote, bundle the data
	}
	// --contains reflects the last local fetch: stale-empty just means a
	// bigger bundle; stale-nonempty (force-push) fails the VM fetch loudly.
	if out, err := gitOut(repo, "branch", "-r", "--contains", sha, "--list", "origin/*"); err == nil && out != "" {
		return "origin", fetchable
	}
	return "thin", fetchable
}

// GitHubToken finds a GitHub credential without insisting on any one tool:
// gh's login first, then whatever https credential the user's git already
// pushes with (osxkeychain, credential-store, ...). Never prompts. Sessions
// use it for private-repo fetches and for `git push`/PRs from the VM — the
// only piece of pier that wants a GitHub credential at all.
func GitHubToken() string {
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	fill := exec.Command("git", "credential", "fill")
	fill.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	fill.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := fill.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "password="); ok && t != "" {
			return t
		}
	}
	return ""
}

// originReachable proves, before skipping the bundle, that the exact fetch
// the VM will run works: same https URL, same auth (GH_TOKEN or anonymous).
// Local credential helpers are disabled so a keychain PAT can't vouch for a
// VM that won't have it. Sessions have open egress, so laptop-success is a
// safe proxy; on failure the bundle path takes over — slower, never wrong.
// Covers: private repo without gh logged in, revoked/SSO-gated tokens, etc.
func originReachable(ctx context.Context, url, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"-c", "credential.helper="} // reset the helper list
	if token != "" {
		args = append(args, "-c", `credential.helper=!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f`)
	}
	cmd := exec.CommandContext(ctx, "git", append(args, "ls-remote", "--heads", url)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GH_TOKEN="+token)
	return cmd.Run() == nil
}

// sshOriginUsable reports whether a session could fetch the ssh origin with
// the laptop's ssh agent forwarded: an agent is running with keys, and
// ls-remote over ssh succeeds locally without prompting. Tried only when no
// https credential worked. Agent forwarding is a relay, not a copy — the key
// never leaves the laptop; the VM borrows it for the bootstrap fetch (and for
// pushes while attached).
func sshOriginUsable(ctx context.Context, repo string) bool {
	raw, err := gitOut(repo, "remote", "get-url", "origin")
	if err != nil || !isSSHURL(raw) {
		return false
	}
	if exec.Command("ssh-add", "-l").Run() != nil {
		return false // no agent, or no keys in it — nothing to forward
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", raw)
	cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	return cmd.Run() == nil
}

func isSSHURL(u string) bool {
	return strings.HasPrefix(u, "git@") || strings.HasPrefix(u, "ssh://")
}

var sshGithub = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/](.+?)(?:\.git)?$`)

// fetchURL normalizes a GitHub origin to https (empty for non-GitHub hosts).
func fetchURL(raw string) string {
	if m := sshGithub.FindStringSubmatch(raw); m != nil {
		return "https://github.com/" + m[1]
	}
	if strings.HasPrefix(raw, "https://github.com/") {
		return raw
	}
	return ""
}

var errEmptyBundle = fmt.Errorf("empty bundle")

// exportRef names the temporary ref a create's bundle travels under. Derived
// from the session name so concurrent creates in the same repo (two quick
// TUI spawns) don't race on one shared ref — the first create's deferred
// delete used to yank it out from under the second's `git bundle create`.
// Slashes flatten so no ref component can start with a dot the name
// validation only blocks at position zero.
func exportRef(name string) string {
	return "refs/pier/export-" + strings.ReplaceAll(name, "/", "-")
}

// gitBundle packs history reachable from sha into a bundle exposing a single
// ref (the create's exportRef) that the VM fetches — includes uncommitted
// nothing, clean by construction. thin subtracts everything origin already
// has, leaving prerequisites the VM satisfies by fetching origin first.
func gitBundle(repo, sha, dst, ref string, thin bool) error {
	if _, err := gitOut(repo, "update-ref", ref, sha); err != nil {
		return err
	}
	defer gitOut(repo, "update-ref", "-d", ref)
	args := []string{"-C", repo, "bundle", "create", dst, ref}
	if thin {
		args = append(args, "--not", "--remotes=origin")
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "empty bundle") {
			return errEmptyBundle
		}
		return fmt.Errorf("git bundle: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// dirtyPatch captures uncommitted work on tracked files — edits, staged
// adds, deletions, binary-safe — as one patch the bootstrap applies right
// after checkout, so the session's working tree starts exactly as the
// laptop's (staged edits arrive unstaged). Non-ignored untracked files travel
// alongside this patch in the files tar. A clean tree writes nothing.
func dirtyPatch(repoRoot, dst string) (bool, error) {
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--binary", "HEAD").Output()
	if err != nil {
		return false, fmt.Errorf("git diff: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return false, nil
	}
	return true, os.WriteFile(dst, out, 0o600)
}

func fileMB(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	return fmt.Sprintf("%.1f MB", float64(fi.Size())/1e6)
}

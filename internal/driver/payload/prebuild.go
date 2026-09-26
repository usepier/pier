package payload

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usepier/pier/internal/driver"
)

// --- prebuild -------------------------------------------------------------------
// A toolchain-only image leaves every create to redo the repo's own setup —
// dependency installs, image builds, seeds — which is most of the wait for any
// real repo. A prebuild pays that once, at bake: the bake instance checks the
// repo out exactly like a session would, runs .pier/setup.sh to completion,
// scrubs everything pier pushed, and only then gets imaged. Sessions launched
// from it find the checkout and setup's artifacts already on disk; their
// bootstrap fetches the delta and setup re-runs warm.

// PrebuildBranch is the placeholder branch the bake instance's checkout sits
// on. A session's bootstrap moves the checkout to its own branch and deletes
// this one.
const PrebuildBranch = "pier-prebuild"

// SetupWait bounds how long a prebuild or a pool fill waits for
// .pier/setup.sh. A setup this slow is broken, not warming.
const SetupWait = 45 * time.Minute

// Remote is the slice of a driver a prebuild drives: copy one file onto the
// bake instance, run one script on it (extra carries ssh flags such as -A).
type Remote struct {
	Push func(ctx context.Context, local, remote string) error
	Run  func(ctx context.Context, extra []string, script string) (string, error)
}

// Prebuild turns a bake instance (harnesses and bake hook already in place)
// into a setup-complete checkout of repoRoot, then scrubs it for imaging.
// The cargo is exactly a create's, off the laptop's HEAD, so setup runs with
// the secrets and env files a real session gets; the scrub then removes every
// file that cargo delivered. What setup itself derived from them (an env
// value compiled into a build, a seeded database) is the repo's to keep out.
func Prebuild(ctx context.Context, repoRoot string, supervisor []byte, manifest []string, env map[string]string, r Remote, progress func(string)) error {
	if progress == nil {
		progress = func(string) {}
	}
	work, err := os.MkdirTemp("", "pier-prebuild-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	spec := driver.CreateSpec{Name: PrebuildBranch, Repo: repoRoot, Branch: PrebuildBranch}
	pl, err := Build(ctx, work, spec, supervisor, manifest, env, progress)
	if err != nil {
		return err
	}
	scrub, err := ScrubScript(filepath.Join(work, "pier-files.tar"), filepath.Base(repoRoot))
	if err != nil {
		return err
	}
	scrubPath := filepath.Join(work, "pier-scrub.sh")
	if err := os.WriteFile(scrubPath, []byte(scrub), 0o755); err != nil {
		return err
	}

	for _, n := range pl.Notes {
		progress(n)
	}
	for _, p := range pl.Pushes {
		if err := r.Push(ctx, p.Local, p.Remote); err != nil {
			return err
		}
	}
	progress("checking out " + filepath.Base(repoRoot))
	var fwd []string
	if pl.ForwardAgent {
		fwd = []string{"-A"}
	}
	if out, err := r.Run(ctx, fwd, "bash /tmp/pier-bootstrap.sh"); err != nil {
		return fmt.Errorf("prebuild checkout: %w\n%s", err, out)
	}
	if hasSetup(repoRoot) {
		progress("running .pier/setup.sh to completion — the slow part every session now skips")
		start := time.Now()
		exec := func(ctx context.Context, cmd string) (string, error) { return r.Run(ctx, nil, cmd) }
		if err := WaitSetup(ctx, exec, SetupWait); err != nil {
			return err
		}
		progress(fmt.Sprintf("setup done in %s", time.Since(start).Round(time.Second)))
	}
	progress("scrubbing secrets and per-session state before imaging")
	if err := r.Push(ctx, scrubPath, "/tmp/pier-scrub.sh"); err != nil {
		return err
	}
	if out, err := r.Run(ctx, nil, "bash /tmp/pier-scrub.sh"); err != nil {
		return fmt.Errorf("prebuild scrub: %w\n%s", err, out)
	}
	return nil
}

// hasSetup mirrors the setup window's choice: the PIER_SETUP_SCRIPT override
// when it resolves, else the repo's .pier/setup.sh.
func hasSetup(repoRoot string) bool {
	if src, _ := setupScriptOverride(repoRoot); src != "" {
		return true
	}
	fi, err := os.Stat(filepath.Join(repoRoot, ".pier", "setup.sh"))
	return err == nil && fi.Mode().IsRegular()
}

// WaitSetup polls the setup status file (the one the supervisor beacons to
// ls/TUI). "running" — and, before the setup window has spun up, a missing
// file — keep it waiting; "0" succeeds; anything else fails with the log tail.
func WaitSetup(ctx context.Context, exec func(ctx context.Context, cmd string) (string, error), timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := exec(ctx, "cat ~/.pier-setup.status 2>/dev/null || echo none")
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err == nil {
			switch status := strings.TrimSpace(out); status {
			case "0":
				return nil
			case "none", "running":
			default:
				tail, _ := exec(ctx, "tail -n 15 ~/.pier-setup.log 2>/dev/null")
				return fmt.Errorf(".pier/setup.sh failed (exit %s):\n%s", status, tail)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("setup still running after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(setupPoll):
		}
	}
}

// setupPoll is WaitSetup's poll interval. A var so tests don't sleep it out.
var setupPoll = 10 * time.Second

// ScrubScript renders the pre-imaging cleanup for a prebuild: every file the
// files tar delivered (harness auth, tokens, the MCP seed, untracked and
// .pier/include repo files) is deleted by exact path, alongside the state
// pier and a first boot leave that must not outlive this instance. Each
// create re-pushes all of it, so none of it buys a session any speed.
func ScrubScript(filesTar, repo string) (string, error) {
	f, err := os.Open(filesTar)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var home, repoFiles []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.ToSlash(filepath.Clean(h.Name))
		if strings.Contains("/"+name+"/", "/../") {
			continue
		}
		switch {
		case strings.HasPrefix(name, "home/"):
			home = append(home, strings.TrimPrefix(name, "home/"))
		case strings.HasPrefix(name, "repo/"):
			repoFiles = append(repoFiles, strings.TrimPrefix(name, "repo/"))
		}
	}

	var b strings.Builder
	b.WriteString(`#!/usr/bin/env bash
# pier prebuild scrub — runs once on the bake instance, right before imaging.
set -uo pipefail
# Processes die with the stop anyway; ending them now keeps the setup log and
# shells from writing past the scrub.
tmux kill-server 2>/dev/null || true
# Containers hold their environment (env_file values included) in their
# config: remove them. Images, volumes and the build cache — the warmth —
# stay, and a session's setup re-run recreates containers in seconds.
ids=$(sudo -n docker ps -aq 2>/dev/null) && [ -n "$ids" ] && sudo -n docker rm -f $ids >/dev/null
cd "$HOME/work/` + repo + `" || exit 1
# Tracked edits the laptop's dirty patch applied; sessions reset anyway.
git reset -q --hard
`)
	for _, p := range repoFiles {
		b.WriteString("rm -f -- " + shQuote(p) + "\n")
	}
	b.WriteString("cd \"$HOME\"\n")
	for _, p := range home {
		b.WriteString("rm -f -- " + shQuote(p) + "\n")
	}
	b.WriteString(`# Credentials setup or the harnesses may have written from the pushed ones,
# and first-boot state a session must create for itself.
rm -rf ~/.config/pier ~/.pier-setup.d
rm -f ~/.pier-bootstrapped ~/.pier-setup.status ~/.pier-setup.log ~/.bash_history \
  ~/.config/gh/hosts.yml ~/.docker/config.json ~/.git-credentials ~/.ssh/agent.sock
rm -f /tmp/pier-scrub.sh
echo scrubbed
`)
	return b.String(), nil
}

// shQuote single-quotes s for bash.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

package payload

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// --- pool claim freshen --------------------------------------------------------
// A warm pool member already carries everything slow: harnesses, supervisor,
// the repo's history, a completed .pier-setup.sh. Claiming it means making
// what's cheap-but-stale current again — this script is that step. It runs on
// the just-resumed member (a boot: the tmux server is always gone, /tmp is
// clean) after the same cargo pushes as create, minus supervisor + user-data.

const freshenTmpl = `#!/usr/bin/env bash
# pier freshen — runs at claim, as agent, on a just-resumed warm pool member.
set -euo pipefail

tar -xf /tmp/pier-files.tar -C "$HOME" --strip-components=1 home 2>/dev/null || true
set -a; . "$HOME/.config/pier/env" 2>/dev/null || true; set +a

cd "$HOME/work/{{REPO}}"
{{GITCONFIG}}
# The transfer decision is re-made at claim — auth can change between fill and
# claim (token added or revoked), and the origin URL follows it.
if [ -n '{{ORIGIN}}' ]; then git remote set-url origin '{{ORIGIN}}' 2>/dev/null || git remote add origin '{{ORIGIN}}'; fi
case '{{ORIGIN}}' in git@*|ssh://*) mkdir -p "$HOME/.ssh" && ssh-keyscan github.com >> "$HOME/.ssh/known_hosts" 2>/dev/null || true ;; esac
{ command -v gh >/dev/null && [ -n "${GH_TOKEN:-}" ] && gh auth setup-git >/dev/null 2>&1; } || true
case '{{MODE}}' in
  origin) git fetch -q --no-tags origin {{SHA}} ;;
  thin)   git fetch -q --no-tags origin && git fetch -q /tmp/pier.bundle {{EXPORTREF}} ;;
  *)      git fetch -q /tmp/pier.bundle {{EXPORTREF}} ;;
esac
# -f discards the member's fill-time tracked state; untracked setup artifacts
# (node_modules, caches — the warmth being claimed) survive. The placeholder
# branch dies once the real one exists (-qD fails harmlessly if checked out).
git checkout -qf -B '{{BRANCH}}' {{SHA}}
git reset -q --hard {{SHA}}
git branch -qD '{{OLDBRANCH}}' 2>/dev/null || true
# Uncommitted edits to tracked files, exactly as the laptop had them (a
# failed apply fails the claim — better than silently missing work).
if [ -f /tmp/pier-dirty.patch ]; then git apply /tmp/pier-dirty.patch; fi
tar -xf /tmp/pier-files.tar -C . --strip-components=1 repo 2>/dev/null || true

# Cosmetic: prompts show the session, not the pool placeholder.
sudo hostnamectl set-hostname '{{HOSTNAME}}' 2>/dev/null || true

` + tmuxEnsure + setupWindow + `
rm -f /tmp/pier.bundle /tmp/pier-files.tar /tmp/pier-dirty.patch /tmp/pier-freshen.sh
echo freshened
`

// BuildFreshen assembles the claim-time cargo that turns a warm pool member
// into the requested session: refreshed secrets (they rotate), the repo
// update (the same origin/thin/full decision as create — the member's
// fill-time checkout makes origin fetches incremental), the dirty patch, and
// the freshen script. oldBranch is the member's placeholder branch. The
// supervisor timeouts are deliberately NOT reset here: fill leaves a 2m idle
// leash that could self-park the member mid-push, so the claim flow resets
// the conf via Exec right after Resume, before any of this lands.
func BuildFreshen(ctx context.Context, dir string, spec driver.CreateSpec, oldBranch string, manifest []string, env map[string]string, progress func(string)) (*Payload, error) {
	p, mode, sha, origin, err := buildCommon(ctx, dir, spec, manifest, env, progress)
	if err != nil {
		return nil, err
	}
	script := strings.NewReplacer(
		"{{REPO}}", filepath.Base(spec.Repo),
		"{{BRANCH}}", spec.Branch,
		"{{OLDBRANCH}}", oldBranch,
		"{{GITCONFIG}}", gitIdentity(spec.Repo),
		"{{MODE}}", mode,
		"{{SHA}}", sha,
		"{{ORIGIN}}", origin,
		"{{EXPORTREF}}", exportRef(spec.Name),
		"{{HOSTNAME}}", Sanitize(spec.Name),
	).Replace(freshenTmpl)
	fp := filepath.Join(dir, "pier-freshen.sh")
	if err := os.WriteFile(fp, []byte(script), 0o755); err != nil {
		return nil, err
	}
	p.Pushes = append([]Push{{fp, "/tmp/pier-freshen.sh"}}, p.Pushes...)
	return p, nil
}

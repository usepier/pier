package payload

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// --- user data ---------------------------------------------------------------
// One cloud-init template for stock and baked images, on every cloud: every
// install is guarded, so on a baked image runcmd is per-instance config only
// (seconds).

const userDataTmpl = `#cloud-config
hostname: {{HOSTNAME}}
users:
  - name: agent
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    ssh_authorized_keys:
      - {{PUBKEY}}
write_files:
  - path: /etc/pier/supervisor.conf
    permissions: "0644"
    content: |
      idle_timeout={{IDLE}}
      unattended_cap={{CAP}}
  # Container root writes files as agent, the way Docker Desktop translates
  # ownership on a Mac. Without this, any compose service running as root
  # litters bind-mounted repos with root-owned files that break host-side
  # tooling. Written before docker installs, so the daemon starts remapped.
  - path: /etc/docker/daemon.json
    permissions: "0644"
    content: |
      {"userns-remap": "agent"}
runcmd:
  - |
    set -x
    export DEBIAN_FRONTEND=noninteractive
    # unattended-upgrades can hold the dpkg lock right after boot; wait it
    # out instead of failing the install (the block deliberately has no set
    # -e, so a lost race would otherwise skip a harness silently).
    echo 'DPkg::Lock::Timeout "120";' > /etc/apt/apt.conf.d/90pier
    install -d -m 700 -o agent -g agent /home/agent/.ssh
    grep -qxF '{{PUBKEY}}' /home/agent/.ssh/authorized_keys 2>/dev/null || echo '{{PUBKEY}}' >> /home/agent/.ssh/authorized_keys
    chown agent:agent /home/agent/.ssh/authorized_keys && chmod 600 /home/agent/.ssh/authorized_keys
    # userns-remap needs subordinate id ranges: the first line maps container
    # uid/gid 0 to agent (1000), the rest of the range covers other ids.
    for f in /etc/subuid /etc/subgid; do
      grep -q '^agent:1000:1$' "$f" 2>/dev/null && continue
      { echo 'agent:1000:1'; cat "$f" 2>/dev/null; } > "$f.pier" && mv "$f.pier" "$f"
      grep -Eq '^agent:[0-9]+:65536$' "$f" || echo 'agent:300000:65536' >> "$f"
    done
    # Guard on packages stock Ubuntu lacks — it already ships tmux/git/jq/curl.
    # docker.io is the bare engine: compose and buildx are separate packages
    # (Docker Desktop/OrbStack bundle them, so "docker compose up" is table stakes).
    { command -v docker && command -v make && docker compose version && docker buildx version; } >/dev/null 2>&1 || { apt-get update -y && apt-get install -y tmux git curl jq unzip ca-certificates docker.io docker-compose-v2 docker-buildx make; }
    # Images baked before the remap existed have docker running unmapped: apply.
    docker info 2>/dev/null | grep -q userns || systemctl restart docker
    command -v node >/dev/null || { curl -fsSL --retry 3 https://deb.nodesource.com/setup_22.x | bash - && apt-get install -y nodejs; }
    command -v gh >/dev/null || { curl -fsSL --retry 3 https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && apt-get update -y && apt-get install -y gh; }
    # claude self-updates, so it must live user-side: under a root-owned npm
    # prefix the updater can only fail, painting a red "Auto-update failed"
    # across every launch. This also keeps sessions from baked images current —
    # they update themselves instead of pinning the bake-time version. The
    # /usr/local/bin symlink keeps claude on PATH for non-login shells (ssh
    # exec channels, like pier mcp login rides).
    if ! command -v claude >/dev/null; then
      sudo -Hu agent bash -c 'curl -fsSL --retry 3 https://claude.ai/install.sh | bash'
      ln -sf /home/agent/.local/bin/claude /usr/local/bin/claude
    elif [ -e /usr/lib/node_modules/@anthropic-ai/claude-code ]; then
      # Images baked before this fix carry claude under root's npm prefix:
      # silence the doomed self-updater there until a re-bake.
      grep -q DISABLE_AUTOUPDATER /etc/environment || echo 'DISABLE_AUTOUPDATER=1' >> /etc/environment
    fi
    command -v codex >/dev/null || npm install -g @openai/codex
    # Headless chromium for browser MCPs/skills (playwright cache + shared libs).
    [ -e /home/agent/.cache/ms-playwright ] || { npx -y playwright install-deps chromium && sudo -Hu agent npx -y playwright install chromium; }
    getent group docker >/dev/null && usermod -aG docker agent
    grep -q 'pier/env' /home/agent/.bashrc || printf '\n[ -f ~/.config/pier/env ] && set -a && . ~/.config/pier/env && set +a\n[ -S ~/.ssh/agent.sock ] && export SSH_AUTH_SOCK=~/.ssh/agent.sock\ncd ~/work/* 2>/dev/null || true\n' >> /home/agent/.bashrc
`

// RenderUserData fills the shared cloud-init template with this session's
// hostname, ssh pubkey, and supervisor timeouts.
func RenderUserData(spec driver.CreateSpec, pubkey string) string {
	return strings.NewReplacer(
		"{{HOSTNAME}}", Sanitize(spec.Name),
		"{{PUBKEY}}", pubkey,
		"{{IDLE}}", durConf(spec.IdleTimeout),
		"{{CAP}}", durConf(spec.UnattendedCap),
	).Replace(userDataTmpl)
}

func durConf(d time.Duration) string {
	if d <= 0 {
		return "never"
	}
	return d.String()
}

// --- bootstrap ----------------------------------------------------------------

const bootstrapTmpl = `#!/usr/bin/env bash
# pier bootstrap — runs once, as agent, on the fresh instance.
set -euo pipefail

# Stock image: harness install still running under cloud-init; wait it out.
# Guard on binaries the stock image LACKS: Ubuntu ships git and tmux, so
# guarding on those skipped the wait everywhere (the same trap the
# user-data idempotency guards document) and .pier-setup.sh raced the
# node/docker/claude installs it depends on.
command -v docker >/dev/null && command -v node >/dev/null && command -v claude >/dev/null || sudo cloud-init status --wait >/dev/null || true

sudo install -m 0755 /tmp/pier-supervisor /usr/local/bin/pier-supervisor
sudo tee /etc/systemd/system/pier-supervisor.service >/dev/null <<'UNIT'
[Unit]
Description=pier session supervisor (self-park watchdog)
After=multi-user.target

[Service]
User=agent
RuntimeDirectory=pier
ExecStart=/usr/local/bin/pier-supervisor
Restart=always

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now pier-supervisor.service

tar -xf /tmp/pier-files.tar -C "$HOME" --strip-components=1 home 2>/dev/null || true
set -a; . "$HOME/.config/pier/env" 2>/dev/null || true; set +a

mkdir -p "$HOME/work/{{REPO}}"
cd "$HOME/work/{{REPO}}"
git init -q -b '{{BRANCH}}'
{{GITCONFIG}}
if [ -n '{{ORIGIN}}' ]; then git remote add origin '{{ORIGIN}}'; fi
# ssh origin = fetch rides the laptop's forwarded agent; pre-trust github.
case '{{ORIGIN}}' in git@*|ssh://*) mkdir -p "$HOME/.ssh" && ssh-keyscan github.com >> "$HOME/.ssh/known_hosts" 2>/dev/null || true ;; esac
# gh brokers git credentials for https origin fetches; on a stock image it may
# still be installing under cloud-init — wait only when it's load-bearing.
if [ '{{MODE}}' != full ] && [ -n "${GH_TOKEN:-}" ] && ! command -v gh >/dev/null; then sudo cloud-init status --wait >/dev/null || true; fi
{ command -v gh >/dev/null && [ -n "${GH_TOKEN:-}" ] && gh auth setup-git >/dev/null 2>&1; } || true
case '{{MODE}}' in
  origin) git fetch -q --no-tags origin {{SHA}} ;;
  thin)   git fetch -q --no-tags origin && git fetch -q /tmp/pier.bundle {{EXPORTREF}} ;;
  *)      git fetch -q /tmp/pier.bundle {{EXPORTREF}} ;;
esac
git reset -q --hard {{SHA}}
# Uncommitted edits to tracked files, exactly as the laptop had them (a
# failed apply fails the create — better than silently missing work).
if [ -f /tmp/pier-dirty.patch ]; then git apply /tmp/pier-dirty.patch; fi
tar -xf /tmp/pier-files.tar -C . --strip-components=1 repo 2>/dev/null || true

# Codex records folder trust in config.toml; pre-trust the workdir the user
# created this session from (claude's equivalent rides the ~/.claude.json seed).
mkdir -p "$HOME/.codex"
grep -q 'work/{{REPO}}' "$HOME/.codex/config.toml" 2>/dev/null || printf '\n[projects."/home/agent/work/{{REPO}}"]\ntrust_level = "trusted"\n' >> "$HOME/.codex/config.toml"

# Terminal scrollback can't reach into tmux, so without mouse mode a session
# reads as "can't scroll up". focus-events feeds terminal focus to the
# programs inside (claude nags about it on every launch otherwise). Seed only
# when no ~/.tmux.conf rode the manifest; must land before the server starts
# below.
if [ ! -f "$HOME/.tmux.conf" ]; then
  printf 'set -g mouse on\nset -g history-limit 50000\nset -g focus-events on\n' > "$HOME/.tmux.conf"
fi

# SSH_AUTH_SOCK points at the attach-refreshed symlink (dangling until the
# first attach forwards an agent; harmless when it never does).
# The server starts through sudo because group membership is snapshotted at
# login: on a stock image cloud-init's "usermod -aG docker agent" lands after
# this ssh session began, so a server started directly here would carry a
# pre-docker group set for its whole life — and every window forks from the
# server, so .pier-setup.sh and the user's shells all get docker.sock denied.
# sudo re-runs initgroups, picking up /etc/group as it stands after the
# cloud-init wait above.
tmux has-session -t main 2>/dev/null || sudo -u agent tmux new-session -d -s main -e "SSH_AUTH_SOCK=$HOME/.ssh/agent.sock" -c "$HOME/work/{{REPO}}"
# Background setup, after checkout + patch + .pier-include extras are all in
# place: the repo's .pier-setup.sh, unless a PIER_SETUP_SCRIPT override rode
# the tar (outer double quotes expand $setup now, into the single-quoted
# bash -c; \$ defers the rest to run time). The outcome must be impossible to
# miss — a failed setup used to vanish with its window: ~/.pier-setup.status
# holds "running" then the exit code (the supervisor beacons it to ls/TUI),
# the log's last line says done/FAILED, and a failed window renames to
# setup-failed and stays open instead of closing. The rename targets its own
# pane id: with a client attached, a bare rename-window can resolve "current
# window" to the attached client's window and mislabel the user's shell.
setup=./.pier-setup.sh
if [ -f "$HOME/.config/pier/setup.sh" ]; then setup="$HOME/.config/pier/setup.sh"; fi
# Presence is the signal, not the exec bit: git only carries +x when the
# author remembered chmod, and gating on -x skipped a committed 0644
# .pier-setup.sh with no trace — the one silent failure setup promises not
# to have. bash runs it either way.
if [ -f "$setup" ]; then
  tmux new-window -d -t main -n setup "bash -c 'set -a; . ~/.config/pier/env 2>/dev/null; set +a; cd ~/work/{{REPO}} || exit 1; echo running > ~/.pier-setup.status; bash $setup 2>&1 | tee ~/.pier-setup.log; c=\${PIPESTATUS[0]}; echo \$c > ~/.pier-setup.status; if [ \$c -eq 0 ]; then echo \"pier setup: done\" >> ~/.pier-setup.log; else echo \"pier setup: FAILED (exit \$c)\" | tee -a ~/.pier-setup.log; tmux rename-window -t \$TMUX_PANE setup-failed; exec sleep infinity; fi'"
fi

# Attach gates on this marker: nobody lands in a half-set-up session. Written
# after the repo checkout and tmux session exist; deliberately NOT after
# .pier-setup.sh, which runs async in its tmux window.
touch "$HOME/.pier-bootstrapped"

rm -f /tmp/pier.bundle /tmp/pier-files.tar /tmp/pier-dirty.patch /tmp/pier-supervisor /tmp/pier-bootstrap.sh
echo bootstrapped
`

func renderBootstrap(spec driver.CreateSpec, mode, sha, origin string) string {
	var gitcfg []string
	line := func(args ...string) {
		out, err := gitOut(spec.Repo, args...)
		if err == nil && out != "" && !strings.Contains(out, "'") {
			switch args[len(args)-1] {
			case "user.name":
				gitcfg = append(gitcfg, "git config user.name '"+out+"'")
			case "user.email":
				gitcfg = append(gitcfg, "git config user.email '"+out+"'")
			}
		}
	}
	line("config", "user.name")
	line("config", "user.email")
	return strings.NewReplacer(
		"{{REPO}}", filepath.Base(spec.Repo),
		"{{BRANCH}}", spec.Branch,
		"{{GITCONFIG}}", strings.Join(gitcfg, "\n"),
		"{{MODE}}", mode,
		"{{SHA}}", sha,
		"{{ORIGIN}}", origin,
		"{{EXPORTREF}}", exportRef(spec.Name),
	).Replace(bootstrapTmpl)
}

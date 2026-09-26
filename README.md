*for local alternative - [wt](https://github.com/kerem-kaynak/wt) preps git worktrees for parallel agent sessions on your machine.*

# ⚓ pier

[![CI](https://github.com/usepier/pier/actions/workflows/ci.yml/badge.svg)](https://github.com/usepier/pier/actions/workflows/ci.yml)

**Give every agent session its own VM. One command up, zero burn when idle.**

Full dev environments on your own AWS or GCP account: your repo, your
secrets, your tools. One command to set up. Detach and it parks itself.
Attach and it's back in under a minute. Outgrown the box? One command
resizes it.

![pier demo](docs/demo.gif)

```
pier setup          # once: pick your cloud, confirm the defaults, bake this repo
cd ~/code/myapp
pier fix-login      # new session: your branch, your secrets, your dev env
pier proxy          # sessions as hostnames: the dev server at fix-login.pier:3000
pier                # the app: sessions, repos, settings
```

## Contents

- [Why pier](#why-pier)
- [The solution](#the-solution)
- [Installation](#installation)
- [Setup](#setup)
- [Usage](#usage)
- [Making your repo pier-ready](#making-your-repo-pier-ready)
- [The pier-onboard skill](#the-pier-onboard-skill)
- [Features](#features)
- [Caveats](#caveats)
- [How it works](#how-it-works)
- [Design decisions](#design-decisions)
- [vs. other tools](#vs-other-tools)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

## Why pier

Coding agents changed what a dev environment is. A session is no longer you
at one machine. It's an agent that runs for hours and wants its own branch,
its own ports, its own docker daemon. And you want several at once.

Every existing place to run them fights you:

- **Your laptop.** Sessions fight over ports and files. Closing the lid
  kills the run.
- **Hosted agent platforms.** Your code, your secrets, and your agent's
  credentials live on someone else's infra, billed by the seat.
- **A cloud dev box.** Bills 24/7 while the agent thinks and while you
  sleep. You still hand-build repo, auth, secrets, and tools for every
  session.

## The solution

pier makes each session a micro-VM that manages its own lifecycle.

```
$ pier checkout-flow
```

One command launches a VM on your cloud account and drops you into tmux
inside it, with:

- your repo on a fresh branch, uncommitted edits included
- Claude Code and Codex installed and authenticated
- your MCP servers configured
- your dev environment built by the repo's own setup script

Detach and forget it. An in-VM supervisor parks the VM once the agent goes
quiet: the instance stops, the disk persists. Attach again and it resumes in
about 20 seconds on AWS and about a minute on GCP, with files, branches, and
credentials exactly as you left them, and your tmux windows back with the
agents reopened on their conversations. If the session outgrows its hardware,
`pier resize` swaps the machine type in one park and resume cycle, disk
intact. And to see what the agent built,
`pier proxy` turns every running session into a hostname:
`checkout-flow.pier:3000` opens in your browser like localhost.

| Session state | Costs |
|---|---|
| running | `~$0.04/h` |
| parked | `~$3-4/mo` (disk only) |
| ready session (parked) | `~$3-4/mo` (disk only) |

There is no control plane. No server, no database, no daemon on your laptop.
Session state lives in instance tags. Every byte between you and the VM is
plain ssh. On AWS it dials the instance directly with port 22 open to your
IP only, and networks that block that path fall back to an SSM tunnel
automatically (`aws.direct = false` forces it). On GCP it rides Google's IAP
tunnel and the instance accepts nothing else from the internet.

**Why AWS and GCP?** Most teams already have the account, the credits, the
budget line, and the compliance review, so pier rides them instead of
introducing a new vendor. VMs are also the right substrate for park/resume:
native stop/start keeps disks intact (measured on EC2: parked in 44s,
resumed in 21s), SSM and IAP give audited access with no bastion, and tags
are a free state store.

## Installation

```
brew install usepier/tap/pier
```

Or build from source with `make`, not `go build` (the in-VM supervisor must
be embedded):

```
git clone https://github.com/usepier/pier
cd pier
make install
```

You also need, on the laptop:

- on AWS: `aws` CLI v2 and the [Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
- on GCP: the [`gcloud` CLI](https://cloud.google.com/sdk/docs/install)
- `git` and OpenSSH (already on macOS/Linux)
- Go 1.24+ (only when building from source)

`pier doctor` checks all of it, plus your cloud auth and account groundwork.

## Setup

```
pier setup
```

The wizard asks as little as it can and applies nothing silently:

1. **Cloud.** Which cloud, then it authenticates against your AWS profile or
   GCP project.
2. **Defaults.** Every default a new session will use, in one block, each
   with what it costs: machine, disk, park-after, runaway cap, speed profile,
   the agent config copied in, and the pier-onboard skill. Press enter to
   accept or `e` to walk through them.
3. **Account.** Creates the groundwork. On AWS: an IAM role and instance
   profile carrying only `AmazonSSMManagedInstanceCore`, plus one egress-only
   security group. On GCP: the compute and IAP API enables, plus two
   firewall rules that admit only Google's IAP range to pier VMs. Then it
   writes `~/.config/pier/config.toml` and runs the `pier doctor` checks.
4. **This repo.** Run inside a repo, it offers to bake the repo's session
   image, so new sessions start in a minute or two instead of running the
   full setup.

Everything it creates is tagged and removable with `pier teardown`. Change
anything later: run `pier` and press `s`.

### Speed profiles

A profile presets two settings: whether pier reminds you to bake, and how
many ready sessions each baked repo keeps.

| Profile | New session | Idle cost per repo |
|---|---|---|
| **Fast** (default) | claims a parked, set-up ready session: ~25s | image ~$1-2/mo + one parked disk |
| **Lean** | boots from the repo's image, setup re-runs warm: ~1-2 min | image only |
| **Minimal** | stock launch, full setup every time | $0 |

A ready session is a stopped VM, so waiting costs disk only. Change either
setting and the profile reads Custom.

No admin rights? `pier setup --print-admin` prints the handful of commands
for an admin to run once. The wizard then works with what exists.

### AWS permissions

Two permission levels exist. Setup needs IAM rights once, to create the
role, the instance profile, and the security group. Daily use needs only:

- `ec2`: run, start, stop, terminate, describe, and create-tags
- `ssm`: start-session and get-parameter
- `sts`: get-caller-identity
- `iam`: PassRole on `pier-session` (plus get-role and get-instance-profile
  for `pier doctor`)
- `ec2` security group ingress calls for direct connect (skipped when
  `aws.direct = false`)

`pier resize` adds `ec2:ModifyInstanceAttribute`. `pier bake` adds
`ec2:CreateImage`, `ec2:DeregisterImage` and `ec2:DeleteSnapshot`. The vCPU
headroom display reads `servicequotas:GetServiceQuota` and degrades politely
without it. The same list ships as comments in `pier setup --print-admin`.

### GCP permissions

Setup needs a project owner or editor once, to enable the compute and IAP
APIs and create two firewall rules. Daily use needs only:

- `roles/compute.instanceAdmin.v1` (instances, disks, images, metadata,
  labels)
- `roles/iap.tunnelResourceAccessor` (the ssh tunnel)

Sessions run with no service account, so no `roles/iam.serviceAccountUser`
grant is needed. The CPU headroom display reads the region quotas, which
`instanceAdmin` already covers. `pier setup --print-admin` prints the exact
setup commands for an admin.

pier locks its VMs down harder than a fresh project does. GCE default
networks ship a rule that opens :22 to the whole internet, and pier VMs hold
an external IP for egress. Setup therefore creates a deny-all ingress rule
for pier VMs and one allow rule above it for exactly Google's IAP range
(`35.235.240.0/20`). Both rules target only instances tagged
`pier-session`. The rest of your network is untouched.

## Usage

```
pier                      the app: Sessions · Repos · Settings tabs (? for keys)
pier <branch> [base]      new session off base (default HEAD), then attach
    -d, --detach          create without attaching
    --idle <dur|never>    park after this much idle time (default from settings)
    --cap <dur|never>     unattended runaway cap (default 8h)
    --no-park             shorthand for --idle never
    --no-ready            launch fresh instead of claiming a ready session
pier ls [--json]          plain list, or stable JSON for automation
pier attach <session>     attach (a parked session resumes, ~20-60s, tmux restored)
pier logs <session>       show the setup script log (-f follows)
pier rm <session> [-f]    destroy the session and its disk
pier keep <session>       pin: never park when idle
pier resize <session> <type>   grow/shrink the VM (~1-2 min, same CPU arch)
pier repos [--json]       every repo: session image, ready sessions, idle cost, tips
pier bake                 build this repo's session image (repo prebuilt by default)
    --toolchain-only      keep repo state out of the image for this bake
pier ready [n]            show, or set, how many ready sessions this repo keeps
pier mcp login <session>  one-time browser approvals for OAuth MCP servers
pier proxy                every running session as <session>.pier (macOS)
pier port <session> <p>   manual port forward (8080:3000 = local:session)
pier setup [--skills]     first-time setup; --skills only refreshes the agent skill
pier doctor               environment + account checks
pier teardown             remove all pier groundwork from the account
```

For automation, `pier ls --json` writes a JSON array with stable fields:
`id`, `name`, `repository`, `branch`, `owner`, `provider`, `state`,
`setup_state`, `strained`, `created_at`, `machine_type`, and `cost_note`.
`created_at` is an RFC 3339 UTC timestamp, or `null` when unavailable;
`setup_state` is empty when no running or failed setup is reported. The JSON
mode writes no ANSI styling or explanatory prose to stdout.

A day with pier:

```
$ cd ~/code/shop && pier checkout-flow
creating checkout-flow (shop @ HEAD)
  ▸ launching t4g.medium (baked image)
  ▸ pushing secrets + repo (GitHub-first: only secrets ride the tunnel)
  ▸ bootstrap: branch checkout-flow, dirty patch applied, setup running
session checkout-flow ready
[tmux opens: claude is installed, authenticated, on your branch]

# ... in a second terminal: the session's ports, one hostname away ...
$ pier proxy
proxy up — running sessions resolve as <session>.pier (http + https), ports mirror on localhost too; ctrl-c to stop
# the agent's dev server, live in your browser: http://localhost:3000
# or, when two sessions hold the same port: http://checkout-flow.pier:3000

# ... detach with C-b d, close the laptop, have dinner ...

$ pier ls
NAME           REPO  STATE   AGE  COST
checkout-flow  shop  parked  3h   ~$3-4/mo

$ pier attach checkout-flow
resuming checkout-flow (~20-60s)...
[right where you left it]
```

## Making your repo pier-ready

Pier uses one committed `.pier/` directory with three optional files:

| File | Runs / read | Contains |
|---|---|---|
| `.pier/setup.sh` | Once at `pier bake` (prebuild), then every session's first boot, async, in a `setup` tmux window | Repo state: deps, services, migrations, seeds (must be safe to re-run) |
| `.pier/include` | At create | Ignored files to carry (env files, local certs) |
| `.pier/bake.sh` | Once, during `pier bake` | Toolchains beyond the default image (pnpm, python, rust, ...) |

Do not ignore `.pier/`: it is shared project configuration. Loose local files
listed in `.pier/include`, such as `.env`, should remain in `.gitignore`.

```bash
# .pier/setup.sh (cwd is the repo root, logs to ~/.pier-setup.log)
set -euo pipefail
pnpm install
docker compose up -d
pnpm db:migrate
```

Prefer `docker compose up -d` for services. A bare background process must
fully detach with `setsid cmd </dev/null >log 2>&1 &` or it dies when the
setup window closes.

```
# .pier/include: one path or glob per line (no **), a directory carries its subtree
.env
apps/*/.env.local
```

A file symlink matched directly by a line or glob is dereferenced: its target
contents arrive as a regular file at the symlink's repository path. Directory
symlinks are never followed.

```bash
# .pier/bake.sh (agent user, passwordless sudo, NO repo checkout yet)
set -euo pipefail
sudo corepack enable
echo COREPACK_ENABLE_DOWNLOAD_PROMPT=0 | sudo tee -a /etc/environment >/dev/null
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack install -g pnpm@10.6.5
```

## The pier-onboard skill

Don't write those files by hand. pier ships
[`skills/pier-onboard`](skills/pier-onboard/SKILL.md), a skill that teaches
a coding agent to inspect your repo, write all three files with the right
boundaries, and keep loose local files protected by `.gitignore`.

`pier setup` installs it with the rest of the defaults, for every agent on
the machine — Claude Code (`~/.claude/skills`) and Codex
(`~/.codex/skills`) read the same skill format — and `pier setup --skills`
refreshes it without the other questions, e.g. after a pier upgrade. To pin it to one repo
instead, copy it into that repo's `.claude/skills/`.

Then ask your agent to "set this repo up for pier".

## Features

### Fast repo transfer, GitHub-first

Tunnels are slow (the SSM one moves about 1 MB/s), so anything big avoids
them:

- Base commit on a GitHub origin: the VM fetches straight from GitHub, only
  secrets ride the tunnel.
- Local-only commits: a thin delta bundle through the tunnel.
- Anything else: a full-history bundle, slow but universal.
- Uncommitted edits to tracked files: one git patch, applied after checkout.

Private repos reuse whatever GitHub credential the laptop already has (`gh`
login, git's https credential helper, or ssh keys through a forwarded
agent). pier verifies the fetch works with exactly the auth the session will
have before skipping the bundle. Pushing from a session works anytime with a
token, and while attached with ssh keys only (the forwarded agent leaves
when you do).

### Secrets, deliberately boring

Local files and secrets travel once, at create, in the files payload:

- `~/.claude`, `~/.codex`, tokens (`gh auth token`, `claude setup-token`)
- non-ignored untracked repo files, plus ignored files your `.pier/include` lists

Untracked files that Git does not ignore ship by default. Ignored files ship
only when `.pier/include` lists them; create prints which ignored env files it
is *not* carrying. The VM never holds cloud credentials. On AWS its instance
role carries SSM and nothing else. On GCP it runs with no service account at
all.

### MCP servers, agents, skills

MCP servers travel with their config, including auth when it's static (env
vars, API-key headers). OAuth-backed remotes keep rotating tokens in the OS
keychain and can't be copied, so they need one browser approval per session:

```
pier mcp login <session>    # sweeps whatever still needs auth
```

Headless Chromium ships in the default image, so browser MCPs and skills
(screenshots, web automation) work out of the box.

### `pier proxy`: sessions as hostnames

```
$ pier proxy
proxy up — running sessions resolve as <session>.pier (http + https), ports mirror on localhost too; ctrl-c to stop
```

- Open `http://checkout-flow.pier:3000` in a browser, or `psql -h checkout-flow.pier`.
- `https://checkout-flow.pier:3000` also works, on the same port.
- Ports the session listens on are discovered and mirrored live.
- Ports also appear on plain `localhost`. Auth callbacks and CORS rules
  that only trust `http://localhost:<port>` keep working unchanged.
- Dev servers are accelerated. The proxy caches modules on your machine
  and prefetches imports in parallel, so vite-style servers load in
  seconds over the WAN instead of one round trip per file. The cache
  warms itself as soon as a port appears, before you open the browser.
  It persists on disk, so a proxy restart starts warm too.
- A live connection counts as attachment, so a session never parks under
  your open browser tab.
- One sudo on first run. macOS only for now.
- `pier port <session> 3000` is the manual, zero-sudo fallback everywhere.

### `pier bake`: per-repo prebuilt images

Most of a create's wait is the repo's own setup: dependency installs,
container image builds, seeds. A toolchain-only image can't help with any of
that, so a bake prebuilds the repo:

```
pier bake    # one throwaway instance: harnesses + .pier/bake.sh, then your
             # checkout (HEAD) with .pier/setup.sh run to completion, imaged
```

Sessions launched from the image find the checkout and everything setup
left around it (installed dependencies, virtualenvs, the docker build cache,
built images, volumes) already on disk. The bootstrap fetches only what
changed, moves the checkout to your branch, and `.pier/setup.sh` re-runs
warm: cache hits instead of a cold install. On AWS the restored volume
is also hydrated at a provisioned rate right after launch, a few cents per
create, so the first commands don't stall on lazily-loaded snapshot blocks.

Before imaging, the bake scrubs everything the create cargo delivered:
harness auth, tokens, the MCP seed, untracked and `.pier/include` files,
containers (their config holds `env_file` values), setup logs. Every create
pushes those fresh, so scrubbing costs no speed. What setup *derives* from
secrets, such as an env value compiled into a build or a seeded database, is
the repo's to keep out. Anyone who can launch the image can read its disk.

Freshness is manual: re-run `pier bake` when dependencies drift enough that
the warm re-run gets slow. Images are keyed to the repo, so one project's
toolchain never bleeds into another's. Re-baking supersedes the old image,
and `pier teardown` sweeps them all by tag.

### Ready sessions: the create is already done

Even a create from a prebuilt image boots a VM and re-runs setup. A ready
session has done that ahead of time: it's a parked, **setup-complete**
session waiting to be claimed. Baked repos keep one by default (the Fast
profile); the Repos tab (`+`/`-`) or `pier ready <n>` changes that per repo.

The next `pier <branch>` claims one instead of creating — resume, check out
your branch, re-push secrets, apply your dirty edits — and refills in the
background from the repo's image. None left just means a normal create:
ready sessions accelerate, never gate, and `--no-ready` skips them for one
create.

Ready sessions are stopped instances, so each costs disk only (~$3-4/mo at
40 GiB). They recycle themselves when they go stale — after a re-bake, a
setup-script change, or 14 days (`pool.max_age`) — and `pier repos` shows
what each repo holds and costs. `pier ready 0` turns them off and removes
the parked ones.

### Rebake reminders

pier never rebakes on its own. With bake reminders on (the Fast and Lean
profiles), it says when a bake would make sessions start faster, and only
states facts: the repo has no image yet, its image is older than the
reminder age (30 days by default), or `.pier/setup.sh` or `.pier/bake.sh`
changed since the bake. Reminders show after a create (at most once a day
per repo), as a dot on the Repos tab, and in `pier repos`.

### The app

`pier` with no arguments opens the app: a **Sessions** tab (every session
with a state dot, a detail pane, attach, logs, resize, keep, delete), a
**Repos** tab (session image, ready sessions, idle cost and reminders per
repo), and **Settings**, one page grouped into Cloud, New sessions, Idle &
cost, and Speed, showing only the active cloud's fields with a line on what
each changes. The header keeps running and parked counts and the live
hourly cost in view; `?` lists the keys.

Every frontend runs on the same API (`pkg/pier`): the CLI, the app, and the
coming Mac app.

### Setup that can't fail silently

`.pier/setup.sh` runs async in its own tmux window on first boot, after the
checkout, dirty patch, untracked files, and `.pier/include` extras are in
place. The outcome always surfaces:

- `pier ls` and the app show `(setup running)` or `(setup failed)`
- `pier logs <session>` prints the log from anywhere, no attach needed
  (`-f` follows, `l` in the app)
- `~/.pier-setup.log` ends with `pier setup: done` or `pier setup: FAILED (exit N)`
- a failed window renames to `setup-failed` and stays open instead of vanishing

Set `PIER_SETUP_SCRIPT` to run a different script for one create.

### Strain and resize

The supervisor reports sustained cpu/mem pressure (kernel PSI), and `pier
resize` fixes it without losing anything:

```
$ pier ls
NAME           REPO  STATE               AGE  COST
checkout-flow  shop  working (strained)  2h   ~$0.04/h

$ pier resize checkout-flow t4g.xlarge
```

Or press `m` in the app. It lists same-arch machines with their vCPU, memory
and hourly cost, so nobody memorizes instance type names.

One park/resume cycle, about a minute on AWS and about two on GCP, disk and
state intact. Deliberately not automatic: the VM holds no cloud credentials,
and auto-scaling is a surprise-cost footgun.

### Honest states

The cloud says "running" long before a session is usable, so pier doesn't:

- A session lists as `creating` until the bootstrap's last act marks the
  instance ready (a tag on AWS, a label on GCP).
- Attaching early gets a plain "still setting up", not a raw connection error.
- Creates from the app run in the background and the row flips when ready.
- A deleted session lists as `deleting` until the cloud actually removes it.
  GCE takes a minute there and would otherwise read as parked.
- A create that fails cleans up its own instance.

## Caveats

- **The SSM fallback tunnel is about 1 MB/s.** Repos that can't come from
  GitHub push their history through it (a 300 MB history takes about 5
  minutes, once per create). The create output says which transfer mode you
  got and why.
- **Parking loses processes.** Files, git state, and installed tools
  survive, and on wake your tmux layout comes back: windows, panes, cwds,
  scrollback, and claude/codex relaunched on their conversations. What was
  running doesn't survive. An agent mid-turn stops at its last saved message,
  and a dev server comes back as its command typed at the prompt, one Enter
  away. Hibernate (park with RAM) is on the roadmap.
- **OAuth MCPs need a browser approval per session.** Keychain-held tokens
  can't be copied safely. This floor is real.
- **ssh-key-only GitHub auth pushes while attached.** The forwarded agent
  disconnects with you. Any token lifts this.
- **`pier proxy` is macOS-only** for now. `pier port` works everywhere.
- **GCP wakes slower than AWS.** Resume to attached is about a minute
  against EC2's ~20s, and a running resize takes about two minutes. GCE
  stop/start simply takes longer.
- **Bake hooks live only in baked images.** A repo with a `.pier/bake.sh`
  that was never baked runs stock. Setup then fails loudly, not silently.
- **Resize stays within the CPU arch** (t4g to t4g). Providers only allow
  type changes on stopped instances.

## How it works

A session is one VM plus its persistent disk, tagged and namespaced by your
caller identity (your complete STS ARN on AWS, including the Identity Center
user segment of an assumed-role ARN), so a team can share one role without
sharing instances. All state lives in those tags and on the disk. `pier ls`
is one filtered describe call, and there is nothing else to operate, back up,
or pay for.

```
laptop                              AWS account (yours)
──────                              ───────────────────
pier CLI/app ── aws cli ──────────▶ EC2 API        (create/stop/start/tags)
     │                              ┌─────────────────────────┐
     └── ssh ─────────────────────▶ │ session VM               │
         (direct to its public IP,  │  tmux ▸ claude / codex   │
          SSM tunnel as fallback)   │  pier-supervisor         │
                                    │  └─ parks the VM when    │
                                    │     detached and quiet   │
                                    └─────────────────────────┘
```

GCP is the same picture with `gcloud`, the GCE API, and Google's IAP tunnel
in place of SSM.

The supervisor samples every 5 seconds for *attached* (tmux clients, or a
live forwarded TCP connection) and *busy* (a running setup script, agent
process-tree CPU, recent pty output):

- attached or busy resets the idle clock
- detached and quiet past `idle_timeout` parks
- detached but busy past `unattended_cap` parks anyway, so a looping agent
  can't burn compute for days

Parking is the VM running `shutdown -h now`, with the instance configured
to stop rather than terminate. The supervisor holds no credentials and calls
no APIs. It beacons state to `/run/pier/status.json`, which `ls`, the TUI,
and `pier proxy` read.

A shutdown ends every process, so every 30 seconds, and once more right
before it parks, the supervisor also snapshots the tmux layout to
`~/.pier/tmux`: sessions, windows, pane layouts and cwds, each pane's last
2000 lines of scrollback, and what each pane was running. On the next boot
`pier-restore.service` rebuilds it before you attach. Scrollback is replayed,
claude comes back with `--resume <id>` (or `--continue`) and codex with
`codex resume --last`, and any other command is typed at the prompt but not
run. It is the same on every cloud: no hibernation, no cloud APIs. Pool
claims and baked images start clean and never inherit a layout.

Full design, including the settled trade-offs and measured spike numbers:
[docs/SPEC.md](docs/SPEC.md).

## Design decisions

The choices contributors should know before proposing changes
(details in [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/SPEC.md](docs/SPEC.md)):

- **No control plane, ever.** State lives in instance tags. Adding a server,
  database, or laptop daemon needs an extraordinary reason.
- **The cloud CLI, not the SDK.** `aws` and `gcloud` are already required
  for their tunnels, and SSO/profiles/MFA come with them for free. v1 has
  no SDK dependency.
- **No cloud credentials in the VM.** Parking is the VM shutting itself
  down. Resize is human-triggered. Anything needing account credentials
  happens from the laptop.
- **One transport.** Attach, exec, file push, and port forwards are all
  OpenSSH, straight to the VM or through the cloud's tunnel. No second
  mechanism to secure or debug.
- **Guarded cloud-init, identical on stock and baked images.** Every
  install step is a no-op when the image already has it. Baking is an
  optimization, never a requirement.
- **Images are prebuilt, secrets are per-session.** `pier bake` images a
  setup-complete checkout so creates skip the repo's cold setup. Anything
  pier pushed is scrubbed before imaging and re-pushed per session, and
  `.pier/setup.sh` must stay safe to re-run on a warm disk.
- **Dirty state travels as a git patch,** not rsync. Binary-safe,
  reviewable, applied atomically after checkout.
- **Truth over optimism in states.** The ready tag, the setup status file,
  the strained flag: pier reports what is, not what the cloud claims.
- **`pier ls` stays plain** (pipeable). The app is the pretty view.
- **One API, many frontends.** Capabilities live in `pkg/pier`, which never
  prints, exits or reads stdin; the CLI, the app and the Mac app render what
  it returns.

## vs. other tools

| | pier | Codespaces / devcontainers | Hosted agent platforms | DIY EC2 + tmux |
|---|---|---|---|---|
| Runs on | your AWS or GCP account | GitHub's infra | vendor's sandbox | your AWS account |
| Idle cost | `~$3-4/mo` (self-parks) | metered, auto-stop | per-seat / per-task | full rate unless you script it |
| Agent-ready | harnesses + auth + MCP travel | you configure | their agent only | you configure |
| Session lifetime | until you `rm` it | workspace-scoped | task-scoped | until you clean it up |
| Interface | your terminal, tmux | VS Code / web | web UI | your terminal |
| Setup | one wizard | per-repo config | account signup | everything by hand |

Honest framing: Codespaces is more polished for *humans editing in VS
Code*. Hosted agent platforms are simpler for *fire-and-forget tasks* when
you don't mind whose infra they run on. pier is for keeping long-lived,
stateful agent sessions on infrastructure you already own and already trust
with your code, at storage prices when you're not using them.

## Roadmap

- **Hibernate/suspend parking.** Keep RAM, resume mid-agent-run.
- **Linux `pier proxy`.**
- **Custom base images.** Bring your own golden image under pier's harness
  layer.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Start with an issue for anything
beyond a small fix. The design ground rules above are load-bearing.

## License

[MIT](LICENSE)

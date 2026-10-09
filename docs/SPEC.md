# pier — v1 design spec

Status: implemented and shipping (v0.2.x), both drivers. The design below
was settled 2026-07-30 (the GCP driver landed 2026-08-08) and stands as the
record of the trade-offs. Where code and spec disagree, the code won.

## 1. Product

Coding-agent sessions (Claude Code / Codex) as micro-VMs on **your own AWS or
GCP account**, parking to near-zero cost when idle and resuming where they
left off. The daily loop:

```
cd ~/code/myapp
pier payments-retry     # new cloud session on branch payments-retry, attached in ~1-2 min
# ... work with claude/codex in the remote tmux, detach or close the tab ...
# the session parks itself once it goes quiet
pier                    # TUI: sessions + states; pick one to reattach (~30-60s from parked)
```

A session comes prepacked with: the repo on a fresh branch, the dev
environment (repo's `.pier/setup.sh`, run automatically), the user's
secrets (one-way copy from the laptop), and both agent harnesses installed.
Everything else is bloat.

**Hard requirements:** runs on AWS *and* GCP easily; scale-to-(near)zero with
pick-back-up; one-command TUI; one-time wizard setup; teams share an account
without collisions; low time-to-first-interaction.

**Non-goals (v1):** sharing/collab features, hosted control plane, web UI,
model-auth brokering (harness configs are copied, nothing more), Windows.

## 2. Session model

Session = **one micro-VM + its persistent disk**. Park = native instance stop
(disk persists, RAM lost). Resume = instance start. Destroy = terminate +
delete disk. Resize = the same park/resume cycle with an instance-type change
in between (providers only allow type changes while stopped — so a strained
session grows with a minute or two of downtime, same CPU arch only). This is
why VMs beat
the container services for this product:

- ECS/Fargate: tasks are immutable and can't stop/resume with local state.
- Cloud Run: no interactive TTY semantics, no persistent local disk.
- Kubernetes: presupposes a cluster (violates easy-setup), no clean
  scale-from-zero per session, docker-in-session is painful. Deferred as a
  possible third driver for teams already living in k8s.

| state    | meaning                                  | cost            |
|----------|------------------------------------------|-----------------|
| creating | provisioning / first boot                | running rate    |
| running  | reachable, a client attached             | ~$0.04/h        |
| working  | reachable, detached, agent busy          | ~$0.04/h        |
| idle     | reachable, quiet; park countdown running | ~$0.04/h        |
| parked   | instance stopped, disk only              | ~$3–4/mo (40GB) |
| dead     | terminated outside pier                  | $0              |

Honest seam: in v1 a park loses running processes (including the tmux server
and any in-flight agent run) — files, git state, and installed tools survive.
What a wake brings back is the arrangement (§6 restore-on-wake): the tmux
layout, cwds and scrollback, with agents relaunched on their conversations
from the transcripts on disk, plus the Docker containers that were running
at the park. What it can't bring back is a process outside Docker that was
mid-flight: an agent turn stops at its last saved message, and a dev server
started in a pane returns as its command typed at the prompt, not running.
Hibernate/suspend was weighed and dropped: it differs per cloud and per
machine family (encrypted roots, launch-time flags, a 60-day cap) and has
no supported guest-initiated trigger. Instead a wake restores the tmux
layout, relaunches agents on their conversations, and restarts the Docker
containers that were running (§6, §7.4).

## 3. Drivers

One Go interface (`internal/driver`), two v1 implementations. All state lives
in provider APIs + tags/labels — **no server, no database, no laptop daemon**.

### aws-ec2
- t4g.medium default (2 vCPU / 4GB, ~$0.034/h), Ubuntu 24.04 arm64 (SSM
  parameter-resolved AMI, or the baked AMI), gp3 root 40GB.
- Default VPC + public IP + `pier-egress-only` SG (direct connect, the
  default, reconciles one TCP-22 rule per caller, /32-scoped to their
  current public IP; `aws.direct = false` keeps it zero inbound). Orgs
  without a default VPC set `subnet` in config.
- Instance profile `pier-session`: `AmazonSSMManagedInstanceCore` only.
- `InstanceInitiatedShutdownBehavior=stop` → in-VM `shutdown -h now` parks.
- Tags: `pier:managed`, `pier:user` (STS ARN), `pier:session`, `pier:repo`,
  `pier:branch`, `pier:origin` (the origin remote as `host/owner/repo`,
  credentials stripped) and `pier:base` (the origin branch the session came
  off). GCE carries the last two as `pier-origin`/`pier-base` metadata. In
  the checkout, `refs/pier/base` and `git config pier.base` record the same
  starting point for in-VM tooling.
- No AWS SDK: the driver shells out to `aws --output json` (the CLI is already
  required for the SSM plugin, and SSO/profiles/MFA come for free).

### gcp-gce
- e2-medium default (2 vCPU / 4GB, ~$0.033/h), Ubuntu 24.04, pd-balanced
  40GB. The same cloud-init user-data as EC2: GCE's Ubuntu images consume
  the `user-data` metadata key, so the create pipeline is shared.
- Default network + external IP (egress only). Two pier-owned firewall
  rules, both scoped to the `pier-session` network tag:
  `pier-allow-iap-ssh` (ALLOW tcp:22 from Google's IAP range
  35.235.240.0/20, priority 999) and `pier-deny-ingress` (DENY all from
  0.0.0.0/0, priority 1000). The deny outranks the permissive rules shared
  networks carry (default networks ship `default-allow-ssh` open to the
  world), so nothing but the IAP tunnel reaches a session.
- Instances run with **no service account, no scopes**.
- `shutdown -h now` lands in TERMINATED with disks intact (= parked).
- The instance name is the session id. Labels mirror the AWS tags
  (`pier-managed`, `pier-user`, ...) for server-side filtering; the label
  charset is strict, so exact values (the caller's principal, repo, branch)
  live in instance metadata and are verified after the filtered list.
- Destroy labels `pier-deleting=1` before the delete call: a GCE delete
  takes a minute+ and mid-delete statuses (STOPPING/TERMINATED) would list
  as parked. The label maps to a `deleting` state instead. EC2 needs none
  of this — its `shutting-down`/`terminated` states are filtered out.

## 4. Attach (and every other byte to the VM)

One mechanism: **OpenSSH over the SSM tunnel** — `ProxyCommand aws ssm
start-session --document-name AWS-StartSSHSession`. Attach is `ssh -t agent@<id>
tmux new -A -s main`; exec is one-shot ssh; file push is scp. Each session
gets its own ed25519 keypair, generated at create, pubkey injected via
cloud-init, private key under `~/.config/pier/keys/`. (GCP: the same raw
OpenSSH with a `gcloud compute start-iap-tunnel --listen-on-stdin`
ProxyCommand and the pubkey in instance metadata — deliberately not
`gcloud compute ssh`, which rides the operator's personal key.)

Zero inbound networking; every connection is IAM-authenticated and audited
(CloudTrail). Inside the VM, work happens as user `agent` in a tmux session
under `/home/agent/work/<repo>`.

Default fast path: connections dial sshd on the instance's public IP
(HostKeyAlias keeps known_hosts keyed by instance id, so the entry survives
the IP change every park/resume brings). The tunnel moves ~100KB/s-1MB/s and
adds a service hop to every keystroke; direct is line-rate and raw-RTT. pier
keeps one ingress rule per caller — TCP 22 from their current public IP /32,
recognized by rule description, stale addresses revoked — and falls back to
the tunnel only when direct can never work (no public IP, network blocks
outbound 22). Dial failures within 3 minutes of launch count as still-booting
and re-probe every few seconds instead, so post-resume connections flip to
direct the moment sshd listens; park/resume/resize drop the probe cache so
nothing dials the previous public IP. `aws.direct = false` forces the tunnel
for every connection. Teardown deletes the SG, rules included.

## 5. Identity & teams

The caller's cloud identity (the complete STS `GetCallerIdentity` ARN; GCP
authed principal) is written to `pier:user` at create and filtered on at list.
For an AWS assumed role, the final role-session segment is retained because it
identifies the Identity Center user. Many devs can therefore choose the same
permission-set role without sharing session state or creating name collisions.

AWS instances created by Pier versions that stripped the role-session segment
have a legacy role-wide `pier:user` tag. The original user cannot be recovered
from that tag; each owner must retag their existing instances once with their
complete current `sts get-caller-identity` ARN after identifying their instance:

```sh
aws ec2 create-tags --resources i-0123456789abcdef0 --tags \
  Key=pier:user,Value="$(aws sts get-caller-identity --query Arn --output text)"
```

## 6. Parking policy (supervisor)

`pier-supervisor` runs in every VM with **zero cloud credentials** — parking
is the VM shutting itself down. Every 5s (fast enough that the beacon's port
list feels live to `pier proxy`; the activity windows still span ~30s) it
derives:

- `attached` — tmux has ≥1 client, **or a live forwarded TCP connection**
  (established sshd-owned sockets that aren't the :22 transport — your
  browser or psql on a mirrored port). A session never parks under an open
  connection; a forgotten tab keeping it awake is deliberately the user's
  problem. App→app loopback traffic (dev server holding a DB pool) doesn't
  count — only sshd's forward dials do.
- `busy` — a chat's turn running, agent process-tree CPU above threshold
  in window (agents started by hand in a terminal), or recent pty output.
  Waiting on the human is not busy: a session parked mid-question says so
  in its chat and goes on with the conversation on the next prompt.

Rules: attached or busy resets the idle clock; detached+quiet past
`idle_timeout` → park; detached+busy past `unattended_cap` → park (runaway
brake, so a looping agent can't burn compute for days). Reason lands in
`/run/pier/status.json` for the TUI. The beacon also carries the session's
load (CPU, memory, disk, load average), which process holds each listening
port, and when it stops on its own (`park_at` while quiet and detached,
`cap_at` while it works unattended), so frontends can say "parks in 12m".

The supervisor also serves `/run/pier/supervisor.sock` (agent-only), relayed
over ssh by `pier-supervisor stream`: frontends follow a session live
(status, and what runs in it — windows, containers, heaviest processes,
ports) on one channel of the session's connection, which never counts as
attached. Changes a frontend asks for (`pier-supervisor op`) run inside the
supervisor rather than the ssh channel that asked, so a dropped connection
can't leave one half done, and each carries an id that makes a retry a
no-op. A session whose supervisor predates the stream gets this pier's on
first contact, the same install a create or claim does.

A session holds any number of **chats**, each a conversation with claude
or codex that the supervisor runs headless as its own child, speaking the
programs' protocols: claude in print mode with stream-json in and out
(`--permission-prompt-tool stdio`, one process per chat), codex as one app
server per session (`codex app-server`, a thread per chat). The programs
are pinned — the versions this pier was built against, downloaded once per
session or image and checked against their published checksums — so a chat
never depends on an agent updating itself into a protocol the supervisor
doesn't speak; what the user runs in a terminal is whatever they installed.
A chat lives in `~/.pier/chats/<id>`: its record (agent, model, effort,
permission mode, the agent's own conversation id), its log of items, and
its prompt queue. The supervisor turns what the agent streams into chat
items (`pkg/pier/chat`), appends them to the log, and streams the text the
agent is writing as deltas. Everything the agent would ask in its terminal
— a tool approval, its questions, its plan — waits on a frontend for as
long as it takes; approving a plan can switch the permission mode, sending
it back carries feedback. A prompt sent mid-turn either joins the running
turn (steer) or waits in the queue for the turn to end, each once by id;
the first prompt of a session's first chat travels in the create's files
and waits for setup. Idle programs are stopped after 20 minutes and resume
their conversation on the next prompt; one that keeps failing puts its chat
in an error state instead of restarting forever. A chat cut off by a park
or a supervisor restart says so and goes on with the next prompt.
Agents started by hand in a terminal stay plain terminal programs: pier
doesn't follow or drive them. The chats show in tmux too: `pier attach
--agent` (or `c` in the TUI) opens a window following them live and
read-only, and `t` there continues the chat shown in the agent's own
interface (`claude --resume`, `codex resume`) — the chat waits meanwhile,
queues what is sent to it, and takes back what happened in the terminal
when the program exits, read from the agent's own record of the
conversation.

The stream carries the chats' state (`chats` topic: what each waits on,
its queue) and, per chat a frontend follows, its log from the offset the
frontend already has (`tail`, pushed as items are appended — no polling)
plus the deltas being written. The laptop keeps a copy of each chat's log
and of the chat list (under the user cache directory, one writer at a
time, capped to its newest items), so chats read while their session is
parked and a reconnect fetches only what is new. The status beacon carries
a summary of each chat — enough for notifications without holding a stream
open to every working session. Replacing the supervisor would end the
chats' programs, so an older one is replaced only while no chat is mid-turn
or waiting.

`pier park <match>` parks a session on demand: a cloud stop, like any other.
A stop from outside reaches the supervisor as systemd's SIGTERM, and it takes
one last tmux snapshot then — its unit stops before pier-restore.service,
which holds the tmux server — so a park from the laptop or a resize keeps
the freshest layout too.

Configurable at wizard time and in config: `idle_timeout` (default 30m,
`"never"` allowed), `unattended_cap` (default 8h, disable-able). Per-session:
`--idle`, `--no-park`, `--cap`, and `pier keep <match>` to pin a session.

The beacon also carries a `strained` flag — kernel PSI (`/proc/pressure/cpu`
avg60 ≥ 40, memory ≥ 10) — which ls/TUI render as `working (strained)`, the
nudge toward `pier resize`. The supervisor never resizes anything itself: the
VM has no cloud credentials (invariant), and auto-scaling is a surprise-cost
footgun. The human is the trigger; the fix is one command.

The beacon additionally lists the session's listening TCP ports (one
`sudo ss -Htnap` pass; sshd and systemd-resolved excluded) — this is how
`pier proxy` knows what to mirror without the user declaring anything.

**Restore-on-wake.** Parking stays a plain shutdown; the supervisor makes it
cheap to come back from. Every ~30s, and once more right before `shutdown`,
it snapshots the agent user's tmux to `~/.pier/tmux/state.json` (atomic
write) from one `tmux list-panes -a` call: every session, window (index,
name, layout string, active) and pane (cwd, active, and the foreground
process's argv read from `/proc` via the pane pid's terminal foreground
group), plus each pane's last 2000 lines of scrollback beside it. No tmux
server leaves the last snapshot alone, so a boot nobody attached to is still
restorable after its next park. Each pane is classified: `agent-claude`
(recording a `--resume`/`-r`/`--session-id` conversation id when argv names
one), `agent-codex`, `setup` (the setup window), `shell`, or `other`.

`pier-restore.service` (installed next to the supervisor unit; oneshot,
`RemainAfterExit=yes`, `KillMode=process`, because the tmux server it starts
lives in its cgroup) runs `pier-supervisor restore` once per boot, before
anyone attaches. When no tmux server runs and a snapshot exists, it rebuilds
every session (`main` first, with the attach-refreshed `SSH_AUTH_SOCK`
symlink), window and pane at its saved cwd and layout. Each pane's shell
replays its scrollback, then: claude relaunches with `claude --resume <id>`,
or `claude --continue` (the newest conversation in that cwd) when no id was
recorded; codex with `codex resume --last`; `other` panes get the old
command typed at the prompt but not executed; setup windows come back as a
plain shell with their log, and setup never re-runs. It always ends by
writing `/run/pier/restored`. Attach waits up to ~15s for that marker, but
only when the unit and a snapshot both exist, so older sessions never wait.
A pool claim is a wake too: freshen drops the restored server and
`~/.pier/tmux` before its own setup window, and the prebuild scrub deletes
`~/.pier/tmux` so an image never carries a layout. No cloud API or
credential is involved, so it works the same on every cloud.

A session either exists fully set up or not at all — no half-states:

- The create's **last act** is a `pier:ready` tag on the instance (written
  after the VM-side bootstrap touches `~/.pier-bootstrapped`; the marker
  stays as the raw-ssh backstop, before the async `.pier/setup.sh`
  finishes). EC2 reports `running` well before the session is usable — SSM
  registration alone lags ~30s — so ls/TUI read running-without-the-tag as
  `creating`: truthful state from the first second, no probe, no SSM
  dependency. Attach and other session commands **refuse cleanly** on a
  creating session ("still setting up — try again when it shows running")
  instead of spawning ssh into a raw `TargetNotConnected` or waiting to
  drop the user into an empty $HOME — which would also steal the `main`
  tmux session away from its workdir. (The supervisor still starts early on
  purpose: even a half-made instance parks itself.)
- A create that fails after launch **destroys its own instance** (ctrl-c
  included — the cleanup runs on a cancel-immune context). Transient scp
  drops right after boot get one retry first.

### 6.1 Hostnames: `pier proxy`

`pier proxy` (foreground, ctrl-c to stop) gives every **running** session its
own hostname: `http://<session>.pier:3000` in the browser, `psql -h
<session>.pier` — real port numbers, any TCP client, zero per-port commands.
All machinery lives in `internal/proxy`; the rest of the product contributes
only `Driver.SSHTarget` (the raw ssh recipe) and the beacon's port list.

- **Names**: a ~100-line UDP responder answers A queries for `*.pier`,
  wired in system-wide via `/etc/resolver/pier` — the split-DNS mechanism
  VPNs use, honored by browsers and getaddrinfo alike. AAAA/HTTPS queries
  get NOERROR-empty (never NXDOMAIN, which would negative-cache the name).
- **IPs**: each session gets a stable private loopback IP (127.94.0.101+,
  32 slots; the resolver sits on 127.94.0.53:5533 — a high port plus a `port`
  directive in the resolver file, because macOS lets unprivileged processes
  bind low ports only on the wildcard address). macOS needs `ifconfig lo0
  alias` per IP — the one sudo, disclosed before it runs; aliases are inert
  /32s that vanish at reboot. Per-session IPs are what let two sessions both
  serve :3000 without colliding — pier's most normal situation.
- **Forwards**: one multiplexed OpenSSH master per running session
  (`-M -S <ctl> -N` over the SSM tunnel). Beacon polls ride the mux as exec
  channels (no new SSM sessions, no AWS API); forwards are added/removed
  live with `ssh -O forward/cancel -L <ip>:<port>:localhost:<port>` as the
  port list changes. Stock OpenSSH, shell-out only — no SSH library.
- **localhost too**: every mirrored port also binds `127.0.0.1:<port>`,
  because origin allow-lists (OAuth dashboards, CORS configs) trust
  `http://localhost:<port>` and nothing else — apps with strict auth
  callbacks work with zero dashboard changes. First session wins a
  contested port (notice printed); the `.pier` name is the tiebreaker.
- **Accelerated HTTP**: the relay sniffs each connection; HTTP is answered
  by a per-port accelerator instead of piped. It caches what the dev server
  marks cacheable (immutable dep chunks forever — the URL hash changes when
  they do — ETagged files with revalidation), and scans JS/HTML responses
  for imports, prefetching the module graph 64-wide over pooled backend
  connections before the browser asks. The moment a port is mirrored the
  accelerator warms itself: one GET / and the crawl fills the cache before
  any browser opens, so even the first load starts warm. A client request
  for something already being prefetched waits for it instead of racing it
  upstream. The cache persists on disk across proxy restarts and parks;
  reloaded entries revalidate against the VM before their bodies are
  reused. Vite-style dev servers (1500 files, 6 browser connections, one
  WAN round trip each = ~36s) load in seconds; a reload becomes one
  parallel revalidation burst. WebSockets, POSTs, streaming and non-HTTP
  traffic pass through untouched.
- **Park-neutral by design**: masters and beacon polls don't touch tmux,
  ptys, or forwarded sockets, so watching a session doesn't keep it awake —
  only actual connections do (§6). Parked sessions' names stop resolving
  rather than auto-resuming: a stray tab must not wake a stopped VM.
- Sessions created before the port-discovery supervisor get a one-time
  warning (recreate to mirror); `pier port` remains the manual, zero-sudo,
  any-OS fallback. macOS-only for now (Linux would need an /etc/hosts block
  or resolved glue — cut until asked for).

## 7. Speed

Targets: **create → attached 60–90s; resume → attached ~30s** (measured
numbers in §14; GCP runs about a minute behind AWS on each — GCE stop/start
and the IAP tunnel are simply slower).

Repo-specific Pier config is committed under `.pier/`; the directory must not
be ignored. Create reads `.pier/include` on the laptop, `.pier/setup.sh`
arrives with the git checkout or the untracked-file payload, and bake reads
`.pier/bake.sh` directly. Ignored local files named by `.pier/include` stay
protected by the repository's `.gitignore`.

1. **`pier bake`** — prebaked **per-repo** image: agent user, tmux, git, gh,
   docker (with compose + buildx — docker.io alone is the bare engine; the
   daemon runs userns-remapped to agent, so container-root writes on bind
   mounts land agent-owned — the ownership translation Docker Desktop does
   on a Mac, without which compose stacks litter repos with root-owned
   files), make,
   claude + codex, headless chromium (playwright build + system
   deps, for browser MCPs/skills), supervisor preinstalled — plus whatever
   the repo's `.pier/bake.sh` installs on top (run on the bake instance as
   agent with passwordless sudo, no repo checkout present: toolchains like
   pnpm/python belong here, repo state in `.pier/setup.sh`; a failed hook
   aborts the bake). Pier deliberately doesn't chase language ecosystems in
   the default image — the hook is the user's channel. Images are keyed by
   repo basename in config (`[aws.baked_amis]` / `[gcp.baked_images]`),
   tagged `pier:repo`; a
   re-bake supersedes and deregisters the repo's previous image, and
   teardown sweeps every `pier:managed`-tagged image. ~$1/mo snapshot
   storage per repo. Offered as the wizard's last step when it runs inside
   a repo; `pier bake` refreshes it.

   **Prebuild (2026-09).** A toolchain-only image turned out to save almost
   nothing on real repos: measured on a large monorepo, a baked create was
   attach-ready in 72s but `.pier/setup.sh` then ran ~9 min (container
   builds with zero cache hits, dependency stores downloaded from scratch),
   so baked and stock felt the same. The bake therefore also prebuilds: after
   the hook, the bake instance receives a create's exact cargo off the
   laptop's HEAD (placeholder branch `pier-prebuild`), bootstraps, and runs
   `.pier/setup.sh` to completion; a failed setup aborts the bake. A scrub
   then deletes, by exact path, every file the files tar delivered, plus
   containers (their config embeds `env_file` values), setup logs and
   status, gh/docker/git credential files and `~/.config/pier`, and resets
   tracked edits. Images, volumes and the build cache stay. Anything setup
   derived from secrets is the repo's responsibility, and is documented as
   such. The bootstrap is shared: finding `~/work/<repo>/.git` it sets the
   origin, fetches incrementally, `checkout -f -B` onto the session branch
   (untracked artifacts survive) and drops the placeholder, so
   `.pier/setup.sh` re-runs warm. That was already the pool-claim contract,
   and is now stated for every session: setup must be safe to re-run on a
   warm disk. Freshness is manual (`pier bake` again); there is no control
   plane to schedule rebakes.

   **Setup runs once per session (2026-09).** Setup runs when a session
   is built — the bootstrap, a pool fill, the bake — and never
   again: a pool claim resumes, checks out the branch and re-pushes
   secrets without it, and a wake re-runs nothing. What a wake needs is what
   was running: the supervisor records the running Docker containers with
   each tmux snapshot (and once more before it parks) in
   `~/.pier/containers.json`, and the first supervisor of each boot waits for
   the daemon and `docker start`s exactly those — any repo, nothing to
   configure. Processes outside Docker come back as the tmux restore types
   them at the prompt.

   EBS restores snapshots lazily from S3, and a prebuilt image is gigabytes.
   AWS launches from a baked image therefore set `VolumeInitializationRate`
   (300 MiB/s, billed per GiB of snapshot data) on the root mapping. A CLI
   that predates the field rejects it client-side, and the launch retries
   without it. GCE documents no lazy-restore penalty, so it needs nothing.
2. **Overlapped create** — launch the instance first; build the git bundle +
   secrets tar while it boots; push and bootstrap the moment sshd answers;
   `.pier/setup.sh` runs asynchronously in a background tmux window while you
   type to the agent — it starts only after the checkout, dirty patch,
   non-ignored untracked files, and `.pier/include` extras are all in place.
   `PIER_SETUP_SCRIPT`
   points it at a different script — relative to the repo root or `~` —
   which travels in the tar and takes precedence over the repo's own.
   The outcome is never silent: the window writes `~/.pier-setup.status`
   ("running", then the exit code — the supervisor beacons it, so `ls`/TUI
   show "(setup running)"/"(setup failed)"), ends `~/.pier-setup.log` with
   `pier setup: done`/`FAILED (exit N)`, and on failure renames itself to
   `setup-failed` and stays open. The status file lives on the disk, so a
   failure stays visible across park/resume.
   (On a stock AMI the bootstrap must wait for cloud-init's
   harness install — minutes, exactly once; bake removes it.)
3. **Origin-first workspace fetch** — the SSM tunnel moves ~1 MB/s, so the
   repo avoids it whenever possible. If the base commit is reachable from a
   GitHub origin ref, the VM fetches straight from GitHub (~100× faster;
   auth via the GH_TOKEN credential helper — ssh origins are rewritten to
   https, which also makes push work in sessions). Local-only commits on top
   of pushed history travel as a thin delta bundle (KBs). Before skipping the
   bundle, a preflight `ls-remote` proves the fetch works with exactly the
   auth a session gets (GH_TOKEN or anonymous; local credential helpers
   disabled). With no usable https credential but a working ssh origin +
   local ssh-agent, the ssh URL is kept and the **agent is forwarded** (`-A`)
   for the bootstrap fetch — the key never leaves the laptop. Attach also
   forwards the agent (symlink-stabilized `SSH_AUTH_SOCK` for tmux), so
   ssh-key users can push while attached; detached pushes still need a token,
   because a persistent capability needs a persistent credential. The
   full-history bundle over SSM remains the universal fallback: no origin,
   non-GitHub host, never-pushed history, all preflights failed. Whatever the
   mode, **uncommitted edits to tracked files** ride along too, as one
   binary-safe patch (`git diff HEAD`) applied right after the checkout — the
   session's working tree starts exactly as the laptop's, staged edits
   arriving unstaged. Non-ignored untracked files ride in the files tar and
   are extracted after checkout + patch. (Both happen only when the session's
   base is the laptop's HEAD; a session created off another commit carries no
   dirty state.) Ignored files travel **only** when **`.pier/include`** names
   them: one path or glob per line, matched against the disk with no
   git-status distinction — listed = travels. Directly matched file symlinks
   are dereferenced into regular files at their repo-relative paths;
   directory symlinks are not followed. The create prints which ignored env
   files it is *not* carrying so a missing one fails loud at create, not deep
   in `make dev`.

4. **Warm pools (per repo)** — baked repos keep
   `speed.pool_size` (default 1, the Fast profile) parked,
   **setup-complete** members ready, overridable per repo with
   `pier pool set <n>` or the app's Repos tab; `pier <branch>`
   then claims one — resume + freshen (secrets re-push, branch at base,
   dirty patch, the claiming pier's supervisor installed with the user's
   timeouts; setup doesn't run again) instead of create + boot + full setup
   — and refills the pool detached in the background. Members are normal
   per-user instances tagged `pier:pool=<gen>`, where gen fingerprints
   driver|image|type|disk|setup-script: a re-bake, config change, or setup
   edit strands the old generation, which recycles at the next claim/fill.
   The pier build is deliberately not part of it: the claim reinstalls the
   supervisor, so a pier upgrade leaves pools claimable;
   members older than `pool.max_age` (default 14d) recycle too, bounding
   repo drift. Concurrent claims race on a nonce written to the claim
   tag/label and read back (providers have no tag CAS); the loser tries the
   next member or falls through to a fresh create — the pool accelerates,
   never gates. Parked members cost disk only (~$3–4/mo each, shown
   wherever pools appear). No daemon holds any of this: a 2m idle leash at
   fill self-parks a member even if the laptop dies mid-fill, and one that
   dies before parking is torn down at the next mutating reconcile (2h
   grace) and flagged by doctor and the pool pages meanwhile — nothing
   burns money silently. Pools are keyed by repo basename, like baked
   images: two checkouts sharing a directory name share a pool, and a
   claim from the "wrong" one fails its freshen loudly and falls back to
   a fresh create.

Cut for v1 (numbers didn't justify the moving parts): mid-create quota
polling.

## 8. Secrets

One-way copy from the laptop at create; never stored anywhere else, never
written back. Sources:

- non-ignored untracked repo files, automatically, plus ignored files named by
  `.pier/include` (path or glob per line). The create warns about ignored env
  files left behind. Directly matched file symlinks carry their target
  contents under the link's repo-relative path. Tracked content arrives with
  the fetch, dirty edits to it via the patch.
- `~/.codex/` (auth.json, config.toml, skills)
- `~/.claude/` settings, `CLAUDE.md`, agents, skills
- a GitHub credential for git push/PRs and private-repo fetch: `gh auth
  token` if gh is logged in, else the laptop's https git credential
  (`git credential fill`), else the ssh agent relayed via forwarding (fetch
  always, push while attached) — never required, doctor reports which was
  found. `~/.ssh` itself never travels.
- Claude subscription auth on macOS lives in the Keychain → one-time
  `claude setup-token` during the wizard, injected as an env var in sessions.
  (Foundry/API-key setups need no token: their auth rides in
  `~/.claude/settings.json` with the manifest — the wizard detects this.)
- a minimal `~/.claude.json` seed: onboarding-done + theme (skips the theme
  picker), the MCP servers that can run on Linux — user-scope ones plus the
  source repo's project-scoped ones, which follow the repo onto the VM
  workdir (auth rides in their env blocks; macOS-binary servers are
  dropped) — and pre-trust for the session workdir so the folder-trust
  dialog never fires. Codex gets the workdir pre-trusted via a `[projects]`
  append to its copied config.toml. History and laptop-path project state
  stay home. MCP servers whose auth lives in the macOS Keychain (OAuth-based
  remotes) carry their declaration but not their tokens — they rotate on
  refresh, so copying them would let two machines revoke each other. Instead:
  they log in from inside the session instead: while someone is attached,
  the attach connection forwards a unix socket the session's `xdg-open` and
  `$BROWSER` (pier-supervisor open) hand URLs to, so they open in the
  laptop's browser, and a side connection mirrors the session's listening
  ports to the same localhost ports, so OAuth callbacks land. Any agent,
  any CLI — nothing agent-specific. Tokens persist on the session disk
  across park/resume. (Remotes that accept static keys
  can be declared locally with an `Authorization` header, which travels
  whole — zero approvals.)

## 9. Setup wizard

`pier setup` — plain stdin prompts (no TUI form), five phases, under 3
minutes:

1. **Detect** — CLIs, profiles/projects, plugin, gh, harness configs; all
   found values become defaults.
2. **Ask** — ≤6 questions (cloud, profile/region or project/zone, size,
   parking policy, secrets manifest, bake now?).
3. **Apply** — idempotent groundwork, printed before it's touched:
   - AWS: role+profile `pier-session` (SSM core policy), SG
     `pier-egress-only` in the default VPC.
   - GCP: enable compute + IAP APIs, firewall rules `pier-allow-iap-ssh`
     + `pier-deny-ingress`.
   `--print-admin` emits exactly this list for an admin when the dev lacks
   IAM rights. `pier teardown` reverses it.
4. **Doctor** — quota headroom, connectivity, plugin; writes
   `~/.config/pier/config.toml`; prints `cd <repo> && pier <branch>`.
5. **Skills** — the pier-onboard skill (embedded in the binary) is one row
   of the confirmed defaults block: accepting installs it for every
   detected agent (claude, codex — same SKILL.md format) under
   `~/.<agent>/skills`; editing asks per agent. An installed dir is
   appended to the session manifest — only when the manifest was accepted
   at all. `pier setup --skills` is the same install standalone.

**Revised flow (2026-09).** Setup now asks only for the cloud and account,
then shows every default in one block — machine, disk, park-after, runaway
cap, speed profile, copied-in config, the skill — with its cost, for enter
(accept) or `e` (walk each). Nothing is applied silently. Inside a repo it
ends by offering the repo's bake, and always by pointing at the app's
settings page (`pier`, then `s`).

Second dev on a prepared account: detect finds groundwork (`Existed`),
creates nothing, done in ~90s.

## 10. Quota & scaling UX

Doctor checks vCPU headroom at setup; every create re-checks opportunistically
during the boot wait (costs nothing). Quota-exceeded errors are caught and
answered with a one-keystroke service-quota increase request. The TUI header
shows headroom (e.g. `12/32 vCPU`).

## 11. CLI

Every frontend — this CLI, the app (`pier` with no arguments), and the
Mac app — runs on `pkg/pier`, which owns the capabilities and never
prints, exits or reads stdin. The Mac app reaches it through `pier api`.

### 11.1 `pier api`

JSON-RPC 2.0 over stdin/stdout, one message per line: a child process of
its client, never a daemon, no port. It exits when its stdin closes, giving
in-flight work three seconds to see its cancellation.

- **Handshake.** `initialize` returns the protocol version, pier's version
  and whether this machine is set up. The Mac app bundles its own pier, so
  a protocol mismatch is a broken install.
- **Calls** run concurrently. `$/cancelRequest {id}` cancels one; long ones
  report `$/progress {id, event}` as they go.
- **Live data** is a subscription. `<topic>.watch` returns
  `{sub, seq, snapshot}` and pushes `sub.event {sub, seq, topic, data}`
  with seq counting up by one, until `sub.cancel {sub}`. A gap means a
  missed event: resubscribe. `overview` carries everything the sidebar,
  Home and the cockpits show, refreshed on the TUI's cadence (fast while
  something changes, slow when quiet, slower when the client reports
  `client.activity {focused: false}`); the stray sweep runs at most every
  ten minutes.
- **Chat.** `chats.watch` lists a session's chats (agent, model, state,
  what each waits on); `chat.watch` sends one chat's newest items, then
  changes (append, update, reset, the chat's state) and the text its agent
  is writing as it streams. A parked session answers from this machine's
  copy with `live: false` and `asOf`; `chat.history` pages older items in.
  `chat.create` starts a chat and `chat.send` sends it a prompt once per
  id — mid-turn, it steers the turn or waits its turn in the queue
  (`chat.dequeue` takes it back); on a parked session, through a
  background job that wakes it. `chat.decide` answers a permission
  request, a question or a plan; `chat.interrupt` stops a turn;
  `chat.configure` changes model, effort or mode; `chat.close` ends a
  chat. `chats.window` and `chat.takeover` open a chat in the session's
  terminal, to watch or to carry on in the agent's own interface.
  `agents.models` lists an agent's models, efforts and modes.
  `files.upload` puts an attachment into the session (or keeps it here
  for a parked one) and `files.list` lists its checkout for @-mentions.
  `notify` tells the client once about a finished turn, a needed answer, a
  failed setup or create.
- **Review.** `git.watch` sends everything a session's branch changed
  against its base (the merge base with the origin branch it came off,
  else the commit recorded at create), committed or not, with line counts
  — then again whenever it changes: the session's supervisor watches the
  checkout (inotify) and pushes, so nothing polls. `git.changes` reads it
  once. `git.diff` gives one file's unified diff, with as much context as
  asked (the whole file). While a session is watched, this machine keeps
  what a parked one shows — its changes, every changed file's diff and
  contents, its file list — reading again only what changed; a parked
  session answers from that copy (`live: false`, `asOf`; read-only).
  `files.read` returns a file (or, `at: base`, the base's copy) with the
  sha of what was read, and `files.write` saves it only if the session's
  copy still has that sha (kind `conflict` otherwise). `search.watch`
  streams matches from the session's ripgrep as they're found; a new
  search ends the last. Reads run in the supervisor too, which finds the
  checkout itself (kind `no_checkout` before there is one). Saves,
  `git.commit` and `git.push` run there — a save is written to a
  temporary file, synced and renamed into place — and each carries an id
  that makes a retry a no-op.
- **Pull requests.** The overview carries each session's pull request —
  found by its branch on the origin's GitHub host, one query per
  repository, with this machine's own login (gh's, else git's) — with its
  checks, review and mergeability, refreshed every 30 seconds while checks
  run, every minute otherwise, every five while the app is out of sight,
  and never into the last of the hour's rate limit. Sessions say why they
  need you (`attention`): an agent waiting, a failed create or setup,
  failing checks, requested changes, a merge to clean up, a pull request
  ready to merge. `pr.get` reads one in full, `pr.log` a check's log,
  `pr.rerun` re-runs failed jobs; `pr.merge` merges only at the head that
  was reviewed (kind `head_moved` otherwise). `chat.compose` writes the
  prompt for fixing CI (with the failing logs), addressing review comments
  or opening a pull request, for the composer. Those prompts, the
  onboarding one and the heading of review comments are the user's to
  word (`prompts.get`, `prompts.set`; `[prompts]` in config.toml), for
  every repo or one (`[prompts.repos.<repo>]`). `sessions.removeMerged` removes a merged
  session only when everything in it is committed and its head is the
  merged one.
- **Setup.** `pier setup`'s steps, for a form: `setup.detect` (tools,
  cloud logins, agent config, the saved answers), `setup.check` (who a
  login is; an expired one is kind `auth_expired` with the login that
  fixes it, and a profile that asks for an MFA code is kind
  `mfa_required` until the code comes with the check), `setup.regions`
  (an account's regions or a project's zones), `setup.defaults` (what a
  draft gives new sessions, with costs), `setup.apply` (groundwork,
  config, checks, the onboarding skill; kinds `no_default_vpc`,
  `no_billing` and `no_permission` say what to do) and `setup.adminDoc`.
  `setup.login` runs a cloud login, narrating its browser prompts; logins
  that want a terminal (gh, a Claude token) run in local ones
  (`term.openLocal`) — pier reads the token `claude setup-token` prints
  and keeps it itself (`term.captured`). `account.teardown` removes pier
  from the account, narrating each step.
  `doctor.run` and `pier doctor --json` give each check the command that
  fixes it.
- **Proxy and ports.** `proxy.start` runs `pier proxy` inside `pier api`,
  on the connections it already keeps (one per session, not a second ssh
  per session); its one-time network setup asks through the macOS
  administrator prompt. `proxy.watch` reports each session's name and
  mirrored ports, `proxy.trustCA` trusts its certificate for https.
  `ports.forward` is `pier port` for the app, lasting as long as pier api
  does. `cli.install` links /usr/local/bin/pier to the app's own pier,
  behind the same prompt when the directory isn't writable.
- **Errors** carry `data.kind` (`auth_expired` with the login command that
  fixes it, `not_set_up`, `parked`, `no_key`, …) so clients never parse
  messages.
- **Jobs.** Creates, bakes, pool fills and resizes run as detached pier
  processes, so they finish whatever happens to the app. Each writes its
  human log plus a JSON-lines event file ending in a `done` or `failed`
  event, and a registry entry under `~/.config/pier/jobs/`. `job.watch`
  streams one; `jobs.list` finds them again after the app restarts;
  `jobs.cancel` stops one the way ctrl-c would, cleanup included. A job
  whose process died without a final event reads `lost`.
- **Shapes** live in `pkg/pier/wire`. `make api-types` regenerates the Mac
  app's TypeScript from them (`internal/api/tsgen`), and CI fails when the
  generated file is stale.
- **Stdout is the protocol's.** The real descriptor moves aside and fd 1
  becomes stderr, so a stray print can only reach the log.
- **Environment.** A GUI-launched process gets launchd's minimal PATH, so
  `pier api` adopts the login shell's PATH and cloud/GitHub variables.

```
pier                    the app: Sessions · Repos · Settings tabs (new = background create,
                        listed as "creating" until the create writes its ready tag)
pier <branch> [base]    create from cwd repo (branch off base, default HEAD) and attach
pier new <branch> [base]  the same, for a branch named like a command
pier ls                 list own sessions
pier attach <match>     reattach; resumes if parked
pier proxy              every running session as <session>.pier, listening ports mirrored
                        live onto a per-session loopback IP and onto localhost, HTTP
                        accelerated (§6.1; macOS, one sudo)
pier port <match> <p> [p...]  manual port forwards, zero-sudo any-OS fallback (3000 or 8080:3000)
pier rm <match>         destroy (instance + disk)
pier keep <match>       disable auto-park for a session
pier resize <match> <type>  change VM size (running: park→modify→resume; same arch)
pier repos [--json]     every repo: session image, pooled sessions, idle cost, reminders
pier pool [set <n> | fill | drain [repo]]  the cwd repo's warm pool
pier setup              wizard (--print-admin for the no-IAM-rights path,
                        --skills to refresh the bundled agent skill only)
pier bake               build/refresh the repo's session image (--toolchain-only)
pier doctor             checks (--json: each with the command that fixes it)
pier teardown           remove account groundwork
```

### 11.2 The Mac app

`macos/`: a React UI in a thin native shell (Tauri), with this pier inside
it as `pier api`'s binary — one release, one version. Everything it shows
comes from `pier api`; the shell only adds what a web view can't do:
notifications whose clicks open the session, the Dock badge, `pier://`
links (`pier://session/<id>`, `pier://repo/<name>`, `pier://new?repo=…`),
and updates. It is a viewer: agents, the prompt queue and every change run
in the session or in a detached pier job, so quitting or crashing mid-turn
loses nothing — prompts not yet acknowledged by their session stay in an
outbox and go out, once, when the app is back.

Releases ship it per CPU arch, signed with a Developer ID and notarized, as
a DMG on the tag's GitHub release and a cask in usepier/homebrew-tap; the
app updates itself from the same release.

## 12. Config (`~/.config/pier/config.toml`)

```toml
driver         = "aws-ec2"
idle_timeout   = "30m"        # or "never"
unattended_cap = "8h"
theme          = "teal"       # accent color: teal navy violet emerald orange crimson pink amber graphite

[aws]
profile       = "default"
region        = "eu-central-1"
instance_type = "t4g.medium"
disk_gib      = 40
# subnet      = "subnet-..."   # only for orgs without a default VPC
# [aws.baked_amis]             # written by `pier bake`, keyed by repo

[gcp]                          # when driver = "gcp-gce"
project      = "my-project"
zone         = "europe-west3-a"
machine_type = "e2-medium"
disk_gib     = 40
# [gcp.baked_images]           # written by `pier bake`, keyed by repo

[secrets]
manifest = [".codex/auth.json", ".codex/config.toml", ".claude/settings.json", ".claude/CLAUDE.md"]
# claude_oauth_token = "..."  # from `claude setup-token` (macOS Keychain path)

[speed]                        # what a speed profile presets (fast/lean/minimal)
pool_size      = 1             # per baked repo; parked, disk-only cost
bake_reminders = true          # suggest `pier bake`; never automatic
image_repo     = true          # bake prebuilds the repo (false = toolchains only)
reminder_age   = "30d"         # remind when an image gets this old

[pool]                         # pooled sessions (§7.4)
# max_age = "14d"              # member recycle age
# [pool.sizes]                 # per-repo overrides, written by `pier pool set`
# shop = 2

# [images.<repo>]              # written by `pier bake`: baked_at, setup_sha,
#                              # bake_sha, repo_included — feeds the reminders
```

## 13. v1 cut line

In: AWS + GCP drivers, TUI, wizard (+print-admin, teardown), bake,
overlapped create, supervisor parking (+keep/pin), one-way secrets copy,
doctor, quota UX, warm pools (opt-in, added post-cut — §7.4).
Out (v1.1+): k8s driver, `ls --all`, Windows.

## 14. Load-bearing bets → spikes

The throwaway spike scripts that produced these numbers have been retired;
the verdicts stand as the record.

| bet                                                        | spike       | status   |
|------------------------------------------------------------|-------------|----------|
| `shutdown -h now` parks (EC2 → stopped, not terminated)    | aws spike | **PASS** — stopped in 44s |
| boot → SSM-ready time supports 60–90s TTFI                 | aws spike | **PASS** — 29s stock AMI |
| stop → start → ready ≈ 30s resume                          | aws spike | **PASS** — 21s |
| SSM interactive TTY feels like ssh (manual, `--keep` mode) | aws spike | superseded — v1 uses real ssh over the SSM tunnel |
| GCE: same four on IAP + TERMINATED-with-disks              | gcp spike | **PASS** — cold IAP-ssh-ready 73s, in-VM shutdown → TERMINATED 58s, resume 56s |

Measured 2026-07-30 in eu-central-1 (AWS) and 2026-08-08 in europe-west3
(GCP).

Full v1 E2E (2026-07-30, eu-central-1, t4g.medium): stock create → ready 42s
(harnesses finish async under cloud-init); idle self-park fired at
idle_timeout+tick; resume → ready 23s; `pier bake` 8m19s once; **baked create
→ attach-ready 52s** — inside the 60-90s TTFI target. keep/pin, ls state
enrichment (beacon), rm, and teardown all verified; account swept clean after.

Full GCP E2E (2026-08-08, europe-west3-a, e2-medium): wizard + setup laid the
groundwork green; stock create → ready ~6 min (the shared user-data works
unchanged — GCE's Ubuntu images consume the `user-data` metadata key, so the
harness install rides the same cloud-init); `pier bake` once, then **baked
create → attach-ready 1m44s**; resume → ready 56s; running resize 1m42s with
creationTimestamp (AGE) preserved; the deny rule verified live — public :22
probes fail while IAP attach works; rm and teardown swept the project back
to pristine.

Warm-pool E2E (2026-08-10/11, eu-central-1, t4g.medium, stock AMI — the test
repo had no bake, so fills paid the cloud-init wait a baked image skips):
`pool set 1` spawned the detached fill itself; fill → parked ready member
2m49s (foreground run; the detached first run landed ~3m10s). **Claim →
session ready 25.8s** (a second claim: 25s) — branch created, dirty patch
carried, setup re-ran to status 0, and the supervisor conf flipped from the
2m fill leash to the user's 1h idle (journal: `parking: idle for 1h0m0s`,
exactly an hour after claim activity ceased). The detached refill built and
parked a replacement unprompted. Two-terminal race against one member: the
winner claimed in 25s, the loser printed `no warm member to claim — creating
fresh` and fell back. The laptop-death backstop got an unplanned live trial:
the laptop's network died mid-refill (even the cleanup terminate couldn't
reach EC2) — the VM finished setup on its own, the fill leash parked it
(`parking: idle for 2m0s`), and that member claimed cleanly the next morning.
Editing `.pier/setup.sh` made the next fill print `recycling stale member …`
and replace it. A repo whose setup script fails on stock (pnpm absent) had
its member destroyed, never parked, and the claim fell back to a fresh
create. Every failed create terminated its own instance; account swept back
to baseline after — instances, volumes, keys, logs, config all gone.

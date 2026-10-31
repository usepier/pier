// pier-supervisor runs inside every session VM as the agent user (systemd
// unit installed at create). It is the entire "control plane": it decides
// when the session parks itself, with zero cloud credentials — parking is
// just `sudo shutdown -h now`, which stops the instance with the disk intact.
//
// Heuristic, checked every 5s (a fast tick keeps the beacon's port list
// fresh for `pier proxy`; the activity windows below still span ~30s):
//
//	attached := tmux reports >=1 client, or a live forwarded TCP connection
//	            (someone's browser/psql on a mirrored port — never park
//	            under an open connection; a forgotten tab keeping the VM
//	            awake is the user's call)
//	busy     := the repo setup script still running (parking mid-setup
//	            would SIGTERM the build and brand the session failed),
//	            any claude/codex process above CPU threshold,
//	            or a pty written to within the last ~30s
//
// attached or busy resets the idle clock. Detached+quiet past idle_timeout
// parks. Detached but busy continuously past unattended_cap parks too (the
// runaway brake). Config is re-read every tick so `pier keep` edits apply
// live. State beacon: /run/pier/status.json (read by ls/TUI via ssh) — also
// carries the listening TCP ports so `pier proxy` knows what to mirror.
//
// Every ~30s, and once more right before parking, it also snapshots the tmux
// layout to ~/.pier/tmux; `pier-supervisor restore` rebuilds it on the next
// boot (tmux.go).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

const (
	confPath   = "/etc/pier/supervisor.conf"
	statusPath = "/run/pier/status.json"
	tick       = 5 * time.Second
	ptyWindow  = 35 * time.Second
	cpuBusyPct = 5.0
	// Strain thresholds on the kernel's PSI avg60 (% of the last minute some
	// task sat stalled on the resource). Surfaced by ls/TUI as a resize hint;
	// the supervisor never acts on it — the VM has no cloud credentials.
	cpuStrainPct = 40.0
	memStrainPct = 10.0
)

type status struct {
	State string    `json:"state"` // attached | working | idle
	Since time.Time `json:"since"`
	// Listening is every user-relevant TCP port the session listens on
	// (sshd/resolved excluded) — `pier proxy` mirrors exactly these. Always
	// present, even empty: its absence means a pre-port-discovery supervisor.
	Listening []int `json:"listening"`
	Tunnels   int   `json:"tunnels,omitempty"` // live forwarded connections
	// Bootstrapping is true until the create's bootstrap writes its marker
	// (repo fetched, tmux up) — ls/TUI keep showing "creating" meanwhile.
	// omitempty keeps old-session beacons (no marker ever) reading as false.
	Bootstrapping bool `json:"bootstrapping,omitempty"`
	Strained      bool `json:"strained,omitempty"`
	// Setup is the repo setup script's outcome: "running" | "failed", empty
	// when there is no script (or it succeeded — quiet is the happy path).
	Setup  string `json:"setup,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Ports names who listens on each port (process and pid), for the
	// environment view; Listening stays the plain list older readers know.
	Ports []port `json:"ports,omitempty"`
	// Res is the session's load this tick.
	Res *resources `json:"res,omitempty"`
	// Renamed is the branch the session named itself after its first
	// prompt, while it's still on it; Branch the checkout's branch once
	// it's pushed as the session's own (branchname.go). The laptop
	// relabels the session after them, Branch first.
	Renamed string `json:"renamed,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// ParkAt is when the session parks if nothing changes: set while it is
	// quiet and detached. CapAt is when the runaway cap stops it: set while
	// it works with nobody attached.
	ParkAt *time.Time `json:"park_at,omitempty"`
	CapAt  *time.Time `json:"cap_at,omitempty"`
	// IdleTimeout and Cap are the configured limits, in seconds (0 = never).
	IdleTimeout int64 `json:"idle_timeout,omitempty"`
	Cap         int64 `json:"cap,omitempty"`
	// Chats are the session's agent chats.
	Chats []wire.ChatSummary `json:"chats,omitempty"`
}

// port is a listening port and who holds it.
type port struct {
	Port    int    `json:"port"`
	Process string `json:"process"`
	PID     int    `json:"pid,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		os.Exit(restoreMain())
	}
	if len(os.Args) > 1 && os.Args[1] == "open" {
		os.Exit(openMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "ps" {
		os.Exit(psMain())
	}
	if len(os.Args) > 1 && os.Args[1] == "stream" {
		os.Exit(streamMain())
	}
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		// Agents an earlier supervisor launched in tmux still run its hooks
		// until they exit: no opinion, never an error.
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "chat" {
		os.Exit(chatMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "install-agents" {
		// The pinned agent programs, now (a bake keeps them in its image).
		home, _ := os.UserHomeDir()
		newPinned(home).installAll()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "op" {
		os.Exit(opMain(os.Args[2:]))
	}
	if len(os.Args) > 1 {
		// Only systemd starts the supervisor itself, with no arguments: a
		// command this one doesn't know must not start a second one.
		fmt.Fprintln(os.Stderr, "pier-supervisor: unknown command", os.Args[1])
		os.Exit(2)
	}

	// A stop from outside (the cloud stopping the instance, a resize) shuts
	// the machine down without asking: systemd's SIGTERM is the last chance
	// to save the tmux layout, as park() does for a park of its own.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go func() {
		<-term
		if !restoring() {
			snapshotNow()
		}
		os.Exit(0)
	}()

	// Bring back the containers the session had running before it parked,
	// alongside the loop: docker may still be starting.
	go func() {
		home, err := os.UserHomeDir()
		if err != nil {
			restartedThisBoot.Store(true)
			return
		}
		if msg := restartContainers(home, 3*time.Minute); msg != "" {
			fmt.Println("pier-supervisor:", msg)
		}
	}()

	idleSince := time.Now()
	detachedBusySince := time.Time{}
	last := status{}
	lastSnap := time.Time{}
	// The branches take three git runs: read every few ticks, not each.
	var branchesAt time.Time
	var renamed, own string
	var cpu cpuSampler
	var procs procSampler
	h := newHub()
	home, _ := os.UserHomeDir()
	host := newChatHost(home)
	h.host = host
	var touchedAt atomic.Int64
	registerChatOps(host, func() { touchedAt.Store(time.Now().UnixNano()) })
	registerTakeoverOps(host)
	host.notify = func() { h.publish("chats", host.topic()) }
	host.deltas = h.deltas
	host.setupRunning = func() bool { return setupState() == "running" }
	host.load()
	h.publish("chats", host.topic())
	go host.pins.installAll()
	go h.watchGit()
	go func() {
		if err := h.serve(); err != nil {
			fmt.Println("pier-supervisor: stream socket:", err)
		}
	}()
	// Prompts go in as soon as their chat is free, not on the slower status
	// tick.
	go func() {
		for {
			host.deliverAll()
			time.Sleep(time.Second)
		}
	}()
	lastTouch := int64(0)

	for {
		idleTimeout, cap_ := readConf()
		listening, ports, tunnels := netSnapshot()
		setup := setupState()
		attached := tmuxAttached() || tunnels > 0
		host.load()
		host.reap()
		host.reapTakeovers()
		busy := setup == "running" || host.turnOpen() || agentBusy() || ptyActive()
		// Someone acting on a chat from a frontend (a prompt, an answer)
		// is a human in the loop: the runaway clock starts over.
		if t := touchedAt.Load(); t != lastTouch {
			lastTouch = t
			detachedBusySince = time.Time{}
		}

		now := time.Now()
		st := "idle"
		switch {
		case attached:
			st = "attached"
		case busy:
			st = "working"
		}
		if attached || busy {
			idleSince = now
		}
		if !attached && busy {
			if detachedBusySince.IsZero() {
				detachedBusySince = now
			}
		} else {
			detachedBusySince = time.Time{}
		}
		if last.State != st {
			last = status{State: st, Since: now}
		}
		last.Listening, last.Ports, last.Tunnels = listening, ports, tunnels
		last.Bootstrapping = bootstrapping()
		last.Strained = strained()
		last.Setup = setup
		res := readResources(&cpu)
		last.Res = &res
		load.add(now, res)
		last.IdleTimeout, last.Cap = int64(idleTimeout.Seconds()), int64(cap_.Seconds())
		last.ParkAt, last.CapAt = nil, nil
		if idleTimeout > 0 && !attached && !busy {
			at := idleSince.Add(idleTimeout)
			last.ParkAt = &at
		}
		if cap_ > 0 && !detachedBusySince.IsZero() {
			at := detachedBusySince.Add(cap_)
			last.CapAt = &at
		}
		last.Chats = host.summaries()
		if now.Sub(branchesAt) >= branchesEvery {
			renamed, own = sessionBranches(home)
			branchesAt = now
		}
		last.Renamed, last.Branch = renamed, own
		writeStatus(last)
		h.publish("status", last)
		if h.watching("env") {
			var e envState
			json.Unmarshal(envSnapshot(&procs), &e)
			e.Ports, e.Res = ports, &res
			h.publish("env", e)
		}

		if now.Sub(lastSnap) >= snapEvery && !restoring() {
			snapshotNow()
			reapTabs()
			lastSnap = now
		}

		if idleTimeout > 0 && !attached && !busy && now.Sub(idleSince) > idleTimeout {
			park("idle for " + idleTimeout.String())
		}
		if cap_ > 0 && !detachedBusySince.IsZero() && now.Sub(detachedBusySince) > cap_ {
			park("unattended cap (" + cap_.String() + ") hit while working detached")
		}

		time.Sleep(tick)
	}
}

// readConf parses KEY=VALUE lines: idle_timeout=30m, unattended_cap=8h.
// "never"/"0"/absent key = disabled. Unreadable file = never park (safe).
func readConf() (idle, cap_ time.Duration) {
	b, err := os.ReadFile(confPath)
	if err != nil {
		return 0, 0
	}
	get := func(key string) time.Duration {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || k != key || v == "never" || v == "0" {
				continue
			}
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
		return 0
	}
	return get("idle_timeout"), get("unattended_cap")
}

func tmuxAttached() bool {
	out, err := exec.Command("tmux", "list-clients").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// agentBusy scans ps for claude/codex processes above the CPU threshold.
func agentBusy() bool {
	out, err := exec.Command("ps", "-eo", "pcpu,args").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		args := strings.Join(f[1:], " ")
		if !strings.Contains(args, "claude") && !strings.Contains(args, "codex") {
			continue
		}
		if strings.Contains(args, "pier-supervisor") {
			continue
		}
		if pcpu, err := strconv.ParseFloat(f[0], 64); err == nil && pcpu >= cpuBusyPct {
			return true
		}
	}
	return false
}

// ptyActive reports whether any pseudo-terminal was written to recently —
// output flowing to a detached tmux pane counts as activity. The window is
// fixed (not tied to the tick) so the faster tick didn't tighten what
// "recently" means.
func ptyActive() bool {
	ents, err := filepath.Glob("/dev/pts/[0-9]*")
	if err != nil {
		return false
	}
	for _, p := range ents {
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) < ptyWindow {
			return true
		}
	}
	return false
}

// bootstrapping reports whether the create's bootstrap has yet to write its
// done-marker. New supervisors only ship on sessions whose bootstrap writes
// it, so a missing marker means "still setting up" (or a create that died —
// either way, not a session to present as ready).
func bootstrapping() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".pier-bootstrapped"))
	return err != nil
}

// setupState reads the setup script's outcome marker, written by the
// bootstrap's tmux window: "running" while the script runs, its exit code
// once it finishes. The file lives in $HOME, so unlike this beacon it
// survives park/resume — a failure stays visible. No marker means no setup
// script (or a pre-marker session): nothing to report.
func setupState() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".pier-setup.status"))
	if err != nil {
		return ""
	}
	switch strings.TrimSpace(string(b)) {
	case "running":
		return "running"
	case "0", "":
		return ""
	default:
		return "failed"
	}
}

// netSnapshot surveys the session's TCP state in one `ss` pass: which ports
// are listening (for the proxy to mirror) and how many live forwarded
// connections exist (they block parking). Process names need root — the
// agent user's NOPASSWD sudo covers it. Any failure reads as "no ports, no
// tunnels": the proxy just sees nothing and parking falls back to the
// tmux/CPU heuristics.
func netSnapshot() ([]int, []port, int) {
	out, err := exec.Command("sudo", "-n", "ss", "-Htnap").Output()
	if err != nil {
		return []int{}, nil, 0
	}
	listening, tunnels := parseSS(string(out))
	return listening, portOwners(string(out), listening), tunnels
}

var ssUser = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// portOwners names the process behind each listening port in ss output.
func portOwners(out string, listening []int) []port {
	want := map[int]bool{}
	for _, p := range listening {
		want[p] = true
	}
	seen := map[int]bool{}
	var ports []port
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "LISTEN" {
			continue
		}
		p := addrPort(f[3])
		if !want[p] || seen[p] {
			continue
		}
		seen[p] = true
		pt := port{Port: p}
		if m := ssUser.FindStringSubmatch(line); m != nil {
			pt.Process = m[1]
			pt.PID, _ = strconv.Atoi(m[2])
		}
		ports = append(ports, pt)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports
}

// parseSS reads `ss -Htnap` output. Listening ports exclude the plumbing
// (sshd, systemd-resolved, containerd's localhost gRPC port — docker-proxy
// stays, published container ports are exactly what to mirror) and dedupe
// v4/v6. A "tunnel" is an established
// connection owned by sshd that isn't the :22 transport itself — exactly the
// loopback dials sshd makes to serve a forwarded port, and never app→app
// traffic (a dev server holding a pooled DB connection must not block
// parking forever).
func parseSS(out string) (listening []int, tunnels int) {
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		local, peer, proc := f[3], f[4], strings.Join(f[5:], " ")
		switch f[0] {
		case "LISTEN":
			if strings.Contains(proc, `"sshd"`) || strings.Contains(proc, `"systemd-resolve`) ||
				strings.Contains(proc, `"containerd`) {
				continue
			}
			if p := addrPort(local); p > 0 && p != 22 && !seen[p] {
				seen[p] = true
				listening = append(listening, p)
			}
		case "ESTAB":
			if strings.Contains(proc, `"sshd"`) && addrPort(local) != 22 && addrPort(peer) != 22 {
				tunnels++
			}
		}
	}
	sort.Ints(listening)
	if listening == nil {
		listening = []int{}
	}
	return listening, tunnels
}

// addrPort pulls the port off ss address forms: 0.0.0.0:22, *:3000,
// 127.0.0.53%lo:53, [::1]:8080.
func addrPort(addr string) int {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0
	}
	return p
}

// strained reports sustained resource pressure. avg60 already smooths over a
// minute, so no extra hysteresis. No /proc/pressure (kernel without PSI) or
// parse trouble reads as 0 = not strained.
func strained() bool {
	return psiAvg60("/proc/pressure/cpu") >= cpuStrainPct ||
		psiAvg60("/proc/pressure/memory") >= memStrainPct
}

func psiAvg60(path string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parsePSIAvg60(string(b))
}

// parsePSIAvg60 pulls avg60 off the "some" line:
//
//	some avg10=0.00 avg60=12.34 avg300=5.00 total=123456
func parsePSIAvg60(s string) float64 {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "some" {
			continue
		}
		for _, kv := range f[1:] {
			if v, ok := strings.CutPrefix(kv, "avg60="); ok {
				if x, err := strconv.ParseFloat(v, 64); err == nil {
					return x
				}
			}
		}
	}
	return 0
}

func writeStatus(s status) {
	_ = os.MkdirAll(filepath.Dir(statusPath), 0o755)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(statusPath, b, 0o644)
}

func park(reason string) {
	writeStatus(status{State: "parking", Since: time.Now(), Reason: reason})
	// The last word on the layout: up to 30s of window and scrollback changes
	// since the previous snapshot would otherwise not come back on wake.
	snapshotNow()
	fmt.Println("pier-supervisor: parking:", reason)
	_ = exec.Command("sudo", "shutdown", "-h", "now").Run()
	os.Exit(0)
}

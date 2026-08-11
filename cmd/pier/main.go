// pier — coding agent sessions as park-when-idle micro-VMs on your own cloud.
package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kerem-kaynak/pier/internal/config"
	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/awsec2"
	"github.com/kerem-kaynak/pier/internal/driver/gcpgce"
	"github.com/kerem-kaynak/pier/internal/driver/payload"
	"github.com/kerem-kaynak/pier/internal/pool"
	"github.com/kerem-kaynak/pier/internal/proxy"
	"github.com/kerem-kaynak/pier/internal/tui"
	"github.com/kerem-kaynak/pier/internal/ui"
	"github.com/kerem-kaynak/pier/internal/wizard"
)

//go:embed assets
var assets embed.FS

// version is stamped by make via -ldflags; "dev" outside a release build.
var version = "dev"

func supervisorBin(arch string) ([]byte, error) {
	b, err := assets.ReadFile("assets/pier-supervisor-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("supervisor for %s not embedded — build with `make`, not `go build`", arch)
	}
	return b, nil
}

const usage = `usage:
  pier                      interactive session list
  pier <branch> [base]      new session off base (default HEAD), attach
      -d, --detach            create without attaching
      --idle <dur|never>      idle self-park timeout (default from config)
      --cap <dur|never>       unattended runaway cap
      --no-park               shorthand for --idle never
      --no-pool               skip this repo's warm pool for this create
  pier ls                   list sessions
  pier attach <session>     attach (auto-resumes if parked)
  pier logs <session>       show the setup script log (-f follows)
  pier mcp login <session>  authenticate every MCP server that still needs it
                            (one browser approval each; add a server name to redo one)
  pier proxy                every running session as <session>.pier — open ports
                            mirrored live on the name and on localhost, dev
                            servers accelerated (macOS; one sudo)
  pier port <session> <port> [port...]  forward ports by hand until ctrl-c
                            (3000 = same both sides, 8080:3000 = local:session)
  pier rm <session> [-f]    destroy session and its disk
  pier keep <session>       pin: disable idle self-park
  pier resize <session> <type>  grow/shrink the VM (running: ~1-2 min park+resume; same arch only)
  pier pool                 warm pool status + cost
  pier pool set <size>      keep <size> warm sessions ready for this repo (0 = off)
  pier pool fill [--detach] top this repo's pool up to size now
  pier pool drain [repo]    destroy a repo's warm members
  pier setup                first-run wizard (creates cloud groundwork)
      --print-admin           print the admin-runnable setup commands instead
  pier doctor               environment + account checks
  pier bake                 prebake this repo's session image (~1-2 min creates)
  pier teardown             remove all pier groundwork from the account
  pier version              print the pier version
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		cmdTUI()
		return
	}
	switch args[0] {
	case "ls":
		cmdLS()
	case "attach":
		cmdAttach(args[1:])
	case "logs":
		cmdLogs(args[1:])
	case "mcp":
		cmdMCP(args[1:])
	case "proxy":
		cmdProxy()
	case "port":
		cmdPort(args[1:])
	case "rm":
		cmdRM(args[1:])
	case "keep":
		cmdKeep(args[1:])
	case "resize":
		cmdResize(args[1:])
	case "pool":
		cmdPool(args[1:])
	case "setup":
		cmdSetup(args[1:])
	case "doctor":
		cmdDoctor()
	case "bake":
		cmdBake()
	case "teardown":
		cmdTeardown()
	case "version", "-v", "--version":
		fmt.Println("pier " + version)
	case "help", "-h", "--help":
		printUsage()
	default:
		cmdNew(args)
	}
}

func printUsage() {
	fmt.Println("\n " + ui.Title.Render("\u2693 pier") +
		ui.Dim.Render(" \u2014 coding agent sessions as park-when-idle micro-VMs on your own cloud") + "\n")
	fmt.Print(usage)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, ui.Bad.Render("pier:"), err)
	os.Exit(1)
}

func newDriver(cfg config.Config) (driver.Driver, error) {
	switch cfg.Driver {
	case "", "aws-ec2":
		return &awsec2.Driver{
			Profile:       cfg.AWS.Profile,
			Region:        cfg.AWS.Region,
			InstanceType:  cfg.AWS.InstanceType,
			DiskGiB:       cfg.AWS.DiskGiB,
			Subnet:        cfg.AWS.Subnet,
			Direct:        cfg.AWS.Direct,
			StateDir:      config.Dir(),
			Manifest:      cfg.Secrets.Manifest,
			SessionEnv:    sessionEnv(cfg),
			SupervisorBin: supervisorBin,
		}, nil
	case "gcp-gce":
		// An empty project would fall through to the operator's active gcloud
		// config — pier must never create resources in whatever project
		// happens to be active.
		if cfg.GCP.Project == "" {
			return nil, fmt.Errorf("gcp.project is not set — run `pier setup`")
		}
		return &gcpgce.Driver{
			Project:       cfg.GCP.Project,
			Zone:          cfg.GCP.Zone,
			MachineType:   cfg.GCP.MachineType,
			DiskGiB:       cfg.GCP.DiskGiB,
			StateDir:      config.Dir(),
			Manifest:      cfg.Secrets.Manifest,
			SessionEnv:    sessionEnv(cfg),
			SupervisorBin: supervisorBin,
		}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", cfg.Driver)
	}
}

// sessionEnv builds ~/.config/pier/env for new sessions: a GitHub credential
// from wherever the laptop already has one (gh login or git's credential
// helper), Claude token from config (macOS keychain escape hatch).
func sessionEnv(cfg config.Config) map[string]string {
	env := map[string]string{}
	if t := cfg.Secrets.ClaudeOAuthToken; t != "" {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = t
	}
	if t := payload.GitHubToken(); t != "" {
		env["GH_TOKEN"] = t
	}
	return env
}

func loadDriver() (config.Config, driver.Driver) {
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	drv, err := newDriver(cfg)
	if err != nil {
		fatal(err)
	}
	return cfg, drv
}

// --- new session ---------------------------------------------------------------

func cmdNew(args []string) {
	// Split positionals from flags so `pier my-branch -d` works (Go's flag
	// package stops at the first positional).
	takesValue := map[string]bool{"-idle": true, "--idle": true, "-cap": true, "--cap": true}
	var pos, flagArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if takesValue[a] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	var detach, noPark, noPool bool
	fs.BoolVar(&detach, "d", false, "")
	fs.BoolVar(&detach, "detach", false, "")
	fs.BoolVar(&noPark, "no-park", false, "")
	fs.BoolVar(&noPool, "no-pool", false, "")
	idleS := fs.String("idle", "", "")
	capS := fs.String("cap", "", "")
	fs.Parse(flagArgs)
	if len(pos) < 1 || len(pos) > 2 {
		printUsage()
		os.Exit(1)
	}
	branch := pos[0]
	base := "HEAD"
	if len(pos) == 2 {
		base = pos[1]
	}

	cfg, drv := loadDriver()
	repo := repoRoot()
	// ctrl-c mid-create must cancel the ctx (not just kill the process) so
	// Create's deferred cleanup can terminate the half-made instance.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sessions, err := drv.List(ctx)
	if err != nil {
		fatal(err)
	}
	for _, s := range sessions {
		if s.PoolGen != "" {
			continue // unclaimed pool members hold placeholder names, not sessions'
		}
		if s.Name == branch {
			fatal(fmt.Errorf("session %q already exists — `pier attach %s`", branch, branch))
		}
	}

	idle, err := config.ParkDuration(or(*idleS, cfg.IdleTimeout))
	if err != nil {
		fatal(fmt.Errorf("--idle: %w", err))
	}
	if noPark {
		idle = 0
	}
	cap_, err := config.ParkDuration(or(*capS, cfg.UnattendedCap))
	if err != nil {
		fatal(fmt.Errorf("--cap: %w", err))
	}

	// Images are repo-specific; a repo that never baked launches from stock
	// (guarded cloud-init installs live).
	image := cfg.BakedImage(filepath.Base(repo))
	fmt.Printf("%s %s\n", ui.Bold.Render("creating "+branch),
		ui.Dim.Render(fmt.Sprintf("(%s @ %s)", filepath.Base(repo), base)))

	// A configured pool turns the create into claim-a-warm-member (resume +
	// freshen, setup already done); an empty or lost pool falls through to the
	// full create below.
	poolable := cfg.PoolSize(filepath.Base(repo)) > 0 && !noPool
	var sess *driver.Session
	if poolable {
		pp, err := poolParams(cfg, drv, repo)
		if err != nil {
			fatal(err)
		}
		// This create's --idle/--cap land on the claimed member's supervisor.
		pp.IdleTimeout, pp.UnattendedCap = idle, cap_
		if sess, err = pool.Claim(ctx, pp, sessions, branch, branch, base); err != nil {
			// The pool accelerates, never gates: whatever broke the claim
			// (bad name, throttled API), the create below gives the same
			// answer or a session.
			fmt.Println(ui.Warn.Render("!") + " claim failed: " + err.Error())
			sess = nil
		}
		if sess == nil {
			fmt.Println(ui.Step("no warm member to claim — creating fresh"))
		}
	}
	if sess == nil {
		sess, err = drv.Create(ctx, driver.CreateSpec{
			Name: branch, Repo: repo, Branch: branch, BaseRef: base, Image: image,
			IdleTimeout: idle, UnattendedCap: cap_,
			Progress: func(step string) { fmt.Println(ui.Step(step)) },
		})
		if err != nil {
			fatal(err)
		}
	}
	if poolable {
		// Top the pool back up in the background — after a claim (a member was
		// consumed) and after a fallback create (the pool was short) alike.
		if logPath, err := spawnPoolFill(repo); err != nil {
			fmt.Println(ui.Warn.Render("!") + ui.Dim.Render(" pool refill didn't start: "+err.Error()))
		} else {
			fmt.Println(ui.Dim.Render("pool refilling in the background — log: " + ui.Tilde(logPath)))
		}
	}
	stop() // create done — ctrl-c back to its default for the prompt + attach
	fmt.Println(ui.OK.Render("session " + sess.Name + " ready"))
	if detach {
		fmt.Println(ui.Dim.Render("attach with: pier attach " + sess.Name))
		return
	}
	// OAuth-backed MCPs need one browser approval each (tokens can't be
	// copied — they rotate); offer the sweep now, while a human is present.
	home, _ := os.UserHomeDir()
	if names := payload.OAuthRemotes(home, repo); len(names) > 0 && stdinIsTTY() {
		if confirm(fmt.Sprintf("mcp %s: run the one-time browser logins now?", strings.Join(names, ", ")), true) {
			loginAll(drv, *sess)
		} else {
			fmt.Println(ui.Dim.Render("later: pier mcp login " + sess.Name))
		}
	}
	attach(drv, sess.ID)
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func repoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fatal(fmt.Errorf("not inside a git repository"))
	}
	return strings.TrimSpace(string(out))
}

// attach runs the interactive ssh+tmux for the CLI paths (pier attach,
// pier <branch>); the TUI has its own tea.ExecProcess flow wired through
// tui.Options so a detach lands back on the list. A fresh or just-resumed VM
// reports cloud-running before its transport answers (EC2's SSM agent takes
// ~30s to register; GCE's IAP tunnel has the same window), so an ssh attempt
// that dies instantly gets one bounded wait-for-reachability and a retry
// instead of a raw transport error dump.
func attach(drv driver.Driver, id string) {
	fmt.Println(ui.Dim.Render("attaching — detach with C-b d (session keeps running)"))
	retried := false
	for {
		cmd, err := drv.AttachCommand(context.Background(), id)
		if err != nil {
			fatal(err)
		}
		start := time.Now()
		runErr := cmd.Run()
		if runErr == nil {
			return
		}
		// ssh uses 255 for a transport failure. Other quick failures came from
		// the remote command (for example tmux rejecting TERM), so waiting for
		// reachability would only hide the real error and run it a second time.
		if retried || !retryAttach(runErr, time.Since(start)) {
			fmt.Fprintln(os.Stderr, ui.Bad.Render("pier:"), "attach:", runErr)
			return
		}
		retried = true
		fmt.Println(ui.Dim.Render("not reachable yet — waiting for the session to come online (fresh VMs take ~30-60s)"))
		if err := waitReachable(drv, id, 4*time.Minute); err != nil {
			fatal(err)
		}
	}
}

func retryAttach(err error, elapsed time.Duration) bool {
	var exitErr *exec.ExitError
	return elapsed <= 15*time.Second && errors.As(err, &exitErr) && exitErr.ExitCode() == 255
}

// waitReachable polls a no-op exec until the driver's ssh transport answers.
func waitReachable(drv driver.Driver, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := drv.Exec(ctx, id, "true")
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("session not reachable after %s — `pier ls` to check on it", timeout)
}

// --- ls / attach / rm / keep -----------------------------------------------------

func cmdLS() {
	_, drv := loadDriver()
	all, err := drv.List(context.Background())
	if err != nil {
		fatal(err)
	}
	// Warm pool members are inventory, not sessions: one dim summary line
	// below the table instead of rows.
	var sessions []driver.Session
	members := 0
	for _, s := range all {
		if s.PoolGen != "" {
			members++
			continue
		}
		sessions = append(sessions, s)
	}
	enrich(drv, sessions)
	poolLine := ""
	if members > 0 {
		poolLine = ui.Dim.Render(fmt.Sprintf("+ %d warm pool member(s) — `pier pool`", members))
	}
	if len(sessions) == 0 {
		fmt.Println(ui.Dim.Render("no sessions — start one with `pier <branch>`"))
		if poolLine != "" {
			fmt.Println(poolLine)
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tREPO\tSTATE\tAGE\tCOST")
	anyStrained, anySetupFailed := false, false
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Repo, stateLabel(s), age(s.Created), s.CostNote)
		anyStrained = anyStrained || s.Strained
		anySetupFailed = anySetupFailed || s.Setup == "failed"
	}
	w.Flush()
	if poolLine != "" {
		fmt.Println(poolLine)
	}
	if anyStrained {
		fmt.Println("\n" + ui.Warn.Render("!") + ui.Dim.Render(" strained = sustained cpu/mem pressure — grow with `pier resize <session> <type>`"))
	}
	if anySetupFailed {
		fmt.Println("\n" + ui.Warn.Render("!") + ui.Dim.Render(" setup failed = the setup script exited nonzero — `pier logs <session>` shows why"))
	}
}

// stateLabel renders the state plus the supervisor's strain and setup flags.
func stateLabel(s driver.Session) string {
	l := string(s.State)
	if s.Strained {
		l += " (strained)"
	}
	switch s.Setup {
	case "running":
		l += " (setup running)"
	case "failed":
		l += " (setup failed)"
	}
	return l
}

// enrich upgrades StateRunning to working/idle by reading each running
// session's supervisor beacon in parallel.
func enrich(drv driver.Driver, sessions []driver.Session) {
	var wg sync.WaitGroup
	for i := range sessions {
		// Pool members run headless between create and park; their beacon
		// detail is nobody's business until they're claimed.
		if sessions[i].State != driver.StateRunning || sessions[i].PoolGen != "" {
			continue
		}
		wg.Add(1)
		go func(s *driver.Session) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := drv.Exec(ctx, s.ID, "cat /run/pier/status.json 2>/dev/null || echo absent")
			if err != nil {
				return // unreachable (ssm blip) — keep plain "running"
			}
			if strings.TrimSpace(out) == "absent" {
				// Only ready-tagged sessions get here, so no beacon isn't
				// mid-create anymore: /run is tmpfs, so right after a resume
				// the supervisor hasn't written its first beacon yet. Keep
				// plain "running".
				return
			}
			var st struct {
				State         string `json:"state"`
				Bootstrapping bool   `json:"bootstrapping"`
				Strained      bool   `json:"strained"`
				Setup         string `json:"setup"`
			}
			if json.Unmarshal([]byte(out), &st) != nil {
				return
			}
			// Supervisor up but bootstrap not done: the repo is still on its
			// way (or the create died) — either way, not attachable yet.
			if st.Bootstrapping {
				s.State = driver.StateCreating
				return
			}
			switch st.State {
			case "working":
				s.State = driver.StateWorking
			case "idle":
				s.State = driver.StateIdle
			}
			s.Strained = st.Strained
			s.Setup = st.Setup
		}(&sessions[i])
	}
	wg.Wait()
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func match(drv driver.Driver, query string) driver.Session {
	sessions, err := drv.List(context.Background())
	if err != nil {
		fatal(err)
	}
	var hits []driver.Session
	for _, s := range sessions {
		if s.PoolGen != "" {
			continue // unclaimed pool members are managed via `pier pool`, not by name
		}
		if s.Name == query {
			return s
		}
		if strings.Contains(s.Name, query) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0]
	case 0:
		fatal(fmt.Errorf("no session matching %q", query))
	default:
		var names []string
		for _, h := range hits {
			names = append(names, h.Name)
		}
		fatal(fmt.Errorf("%q is ambiguous: %s", query, strings.Join(names, ", ")))
	}
	panic("unreachable")
}

func cmdAttach(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier attach <session>"))
	}
	_, drv := loadDriver()
	s := match(drv, args[0])
	requireReady(s)
	resumeIfParked(drv, s)
	attach(drv, s.ID)
}

// cmdLogs prints the session's setup script log without an attach — the
// first question after "(setup failed)" is always "what broke". -f follows
// a still-running setup live.
func cmdLogs(args []string) {
	follow := false
	var rest []string
	for _, a := range args {
		if a == "-f" || a == "--follow" {
			follow = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 {
		fatal(fmt.Errorf("usage: pier logs <session> [-f]"))
	}
	_, drv := loadDriver()
	showLogs(drv, match(drv, rest[0]), follow)
}

// showLogs streams the raw log for `pier logs` — pipeable, and the terminal
// interprets the progress-meter escapes natively. The TUI's l key renders a
// sanitized in-place view instead.
func showLogs(drv driver.Driver, s driver.Session, follow bool) {
	requireReady(s)
	if s.State == driver.StateParked {
		resumeIfParked(drv, s)
		if err := waitReachable(drv, s.ID, 4*time.Minute); err != nil {
			fatal(err)
		}
	}
	remote := `[ -f ~/.pier-setup.log ] && cat ~/.pier-setup.log || echo "no setup log — this session ran no setup script"`
	if follow {
		remote = `[ -f ~/.pier-setup.log ] && exec tail -n +1 -f ~/.pier-setup.log || echo "no setup log — this session ran no setup script"`
	}
	opts, dest, err := drv.SSHTarget(context.Background(), s.ID)
	if err != nil {
		fatal(err)
	}
	cmd := exec.Command("ssh", append(opts, dest, remote)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// A followed log ends with ctrl-c; that interrupt is not an error.
	if err := cmd.Run(); err != nil && !follow {
		fatal(fmt.Errorf("logs: %w", err))
	}
}

// requireReady refuses commands against a still-creating session — cleanly,
// before any ssh is spawned, so the user never sees a raw transport error
// from the window between cloud-running and actually-attachable.
func requireReady(s driver.Session) {
	if s.State == driver.StateCreating {
		fatal(fmt.Errorf("%s is still setting up — try again when `pier ls` shows it running", s.Name))
	}
	if s.State == driver.StateDeleting {
		fatal(fmt.Errorf("%s is being deleted", s.Name))
	}
}

var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9._:@-]+$`)

// cmdMCP: `pier mcp login <session> [server]` — one-time OAuth for MCP
// servers whose tokens live in the laptop's keychain and can't be copied
// (they rotate; two machines sharing one revoke each other). The callback
// port rides the existing SSM tunnel, so the browser approval on the laptop
// completes the flow inside the VM: click, approve, done — no URL copying.
// Without a server name it sweeps everything still unauthenticated.
func cmdMCP(args []string) {
	if len(args) < 2 || args[0] != "login" {
		fatal(fmt.Errorf("usage: pier mcp login <session> [server]"))
	}
	_, drv := loadDriver()
	s := match(drv, args[1])
	requireReady(s)
	resumeIfParked(drv, s)

	if len(args) == 2 {
		loginAll(drv, s)
		return
	}
	server := args[2]
	if !mcpServerName.MatchString(server) {
		fatal(fmt.Errorf("implausible server name %q", server))
	}
	if err := loginOne(drv, s, server); err != nil {
		fatal(fmt.Errorf("mcp login: %w", err))
	}
}

// loginAll asks the session which OAuth-backed MCP servers still lack a
// token (seeded ~/.claude.json minus ~/.claude/.credentials.json) and runs
// the browser flow for each, sequentially — one command, N approvals, and
// re-running it is free: already-authenticated servers are skipped.
func loginAll(drv driver.Driver, s driver.Session) {
	out, err := drv.Exec(context.Background(), s.ID,
		"cat ~/.claude.json 2>/dev/null; printf '\\n---PIER-SPLIT---\\n'; cat ~/.claude/.credentials.json 2>/dev/null")
	if err != nil {
		fatal(err)
	}
	cfgRaw, credRaw, _ := strings.Cut(out, "---PIER-SPLIT---")
	var pending []string
	for _, n := range payload.OAuthRemoteNames([]byte(cfgRaw), payload.Workspace+"/"+s.Repo) {
		if !mcpAuthed(credRaw, n) {
			pending = append(pending, n)
		}
	}
	if len(pending) == 0 {
		fmt.Println(ui.OK.Render("✓") + " every MCP server in " + s.Name + " is authenticated")
		return
	}
	fmt.Printf("%s %s\n", ui.Bold.Render(fmt.Sprintf("%d server(s) need a one-time browser approval:", len(pending))),
		strings.Join(pending, ", "))
	for i, server := range pending {
		fmt.Printf("\n%s %s\n", ui.Accent.Render(fmt.Sprintf("[%d/%d]", i+1, len(pending))), ui.Bold.Render(server))
		if err := loginOne(drv, s, server); err != nil {
			fmt.Fprintln(os.Stderr, ui.Warn.Render("  ! "+server+" didn't finish — retry later with `pier mcp login "+s.Name+" "+server+"`"))
		}
	}
}

func loginOne(drv driver.Driver, s driver.Session, server string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	fmt.Println(ui.Dim.Render("  open the URL below and approve — the callback tunnels into the session; the token survives park/resume"))
	cmd, err := drv.MCPLoginCommand(context.Background(), s.ID, server, port)
	if err != nil {
		return err
	}
	return cmd.Run()
}

// mcpAuthed checks the session's credential store for a token under this
// server. Claude keys mcpOAuth by server name (possibly suffixed); an
// unrecognized format fails open — worst case one redundant approval.
func mcpAuthed(credJSON, server string) bool {
	var c struct {
		McpOAuth map[string]json.RawMessage `json:"mcpOAuth"`
	}
	if json.Unmarshal([]byte(credJSON), &c) != nil {
		return false
	}
	for k := range c.McpOAuth {
		if k == server || strings.HasPrefix(k, server+"|") || strings.HasPrefix(k, server+":") {
			return true
		}
	}
	return false
}

// cmdProxy: `pier proxy` — every running session gets a hostname
// (<session>.pier) with its listening ports mirrored automatically; live
// connections keep the session from parking. All the machinery lives in
// internal/proxy; runs in the foreground until ctrl-c.
func cmdProxy() {
	// net/http logs transport chatter ("Unsolicited response received...")
	// through the global logger when probed ports answer oddly. All of the
	// proxy's real output goes through Options.Out, so drop the rest.
	log.SetOutput(io.Discard)
	_, drv := loadDriver()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := proxy.Run(ctx, drv, proxy.Options{StateDir: config.Dir(), Out: os.Stdout}); err != nil {
		fatal(err)
	}
}

// cmdPort: `pier port <session> <port> [port...]` — hold ssh -L forwards open
// by hand. The zero-sudo, works-anywhere fallback to `pier proxy`; "8080:3000"
// maps local:session.
func cmdPort(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: pier port <session> <port> [port...]   (3000, or local:session like 8080:3000)"))
	}
	_, drv := loadDriver()
	s := match(drv, args[0])
	var pairs [][2]int
	for _, a := range args[1:] {
		p, err := parsePortPair(a)
		if err != nil {
			fatal(err)
		}
		pairs = append(pairs, p)
	}
	resumeIfParked(drv, s)
	for _, p := range pairs {
		fmt.Printf("  %s -> %s\n", ui.Bold.Render(fmt.Sprintf("localhost:%d", p[0])),
			ui.Dim.Render(fmt.Sprintf("%s:%d", s.Name, p[1])))
	}
	fmt.Println(ui.Dim.Render("  forwarding — ctrl-c to stop"))
	cmd, err := drv.PortForwardCommand(context.Background(), s.ID, pairs)
	if err != nil {
		fatal(err)
	}
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("port forward: %w", err))
	}
}

func parsePortPair(s string) ([2]int, error) {
	local, remote, split := strings.Cut(s, ":")
	if !split {
		remote = local
	}
	var p [2]int
	for i, v := range []string{local, remote} {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return p, fmt.Errorf("bad port %q — want 3000 or local:session like 8080:3000", s)
		}
		p[i] = n
	}
	return p, nil
}

// freePort grabs an OS-assigned port. Local and remote must match: the OAuth
// redirect URL embeds the one port claude registers inside the VM.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func resumeIfParked(drv driver.Driver, s driver.Session) {
	if s.State == driver.StateParked {
		fmt.Println(ui.Dim.Render("resuming " + s.Name + " (~20-60s)..."))
		if err := drv.Resume(context.Background(), s.ID); err != nil {
			fatal(err)
		}
	}
}

func cmdRM(args []string) {
	force := false
	var names []string
	for _, a := range args {
		if a == "-f" || a == "--force" {
			force = true
		} else {
			names = append(names, a)
		}
	}
	if len(names) != 1 {
		fatal(fmt.Errorf("usage: pier rm <session> [-f]"))
	}
	_, drv := loadDriver()
	s := match(drv, names[0])
	if !force && !confirm(fmt.Sprintf("destroy session %q and its disk?", s.Name), false) {
		return
	}
	if err := drv.Destroy(context.Background(), s.ID); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render("destroyed " + s.Name))
}

func cmdKeep(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier keep <session>"))
	}
	_, drv := loadDriver()
	s := match(drv, args[0])
	if err := pin(drv, s); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render(s.Name+" pinned") + ui.Dim.Render(" — no idle self-park (unattended cap still applies)"))
}

// pin disables idle self-park; the supervisor re-reads its conf every tick,
// so this applies live.
func pin(drv driver.Driver, s driver.Session) error {
	if s.State == driver.StateParked {
		return fmt.Errorf("%s is parked — `pier attach %s` first", s.Name, s.Name)
	}
	_, err := drv.Exec(context.Background(), s.ID,
		"sudo sed -i 's/^idle_timeout=.*/idle_timeout=never/' /etc/pier/supervisor.conf")
	return err
}

func cmdResize(args []string) {
	if len(args) != 2 {
		fatal(fmt.Errorf("usage: pier resize <session> <instance-type>"))
	}
	_, drv := loadDriver()
	s := match(drv, args[0])
	requireReady(s) // resizing mid-create would stop the instance under its bootstrap
	itype := args[1]
	if s.State == driver.StateParked {
		fmt.Printf("%s %s\n", ui.Bold.Render("resizing "+s.Name+" to "+itype), ui.Dim.Render("(parked — stays parked)"))
	} else {
		fmt.Printf("%s %s\n", ui.Bold.Render("resizing "+s.Name+" to "+itype), ui.Dim.Render("(parks, resizes, resumes ~1-2 min)"))
	}
	if err := drv.Resize(context.Background(), s.ID, itype); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render(s.Name + " is now a " + itype))
}

// --- warm pools --------------------------------------------------------------------

// poolParams assembles everything internal/pool needs from config + driver,
// including the generation fingerprint — which is why it must run from inside
// the repo (the local setup script feeds the hash).
func poolParams(cfg config.Config, drv driver.Driver, repoRoot string) (pool.Params, error) {
	maxAge, err := cfg.PoolMaxAge()
	if err != nil {
		return pool.Params{}, err
	}
	idle, err := config.ParkDuration(cfg.IdleTimeout)
	if err != nil {
		return pool.Params{}, err
	}
	cap_, err := config.ParkDuration(cfg.UnattendedCap)
	if err != nil {
		return pool.Params{}, err
	}
	itype, disk := cfg.AWS.InstanceType, cfg.AWS.DiskGiB
	if cfg.Driver == "gcp-gce" {
		itype, disk = cfg.GCP.MachineType, cfg.GCP.DiskGiB
	}
	repo := filepath.Base(repoRoot)
	image := cfg.BakedImage(repo)
	return pool.Params{
		Driver:      drv,
		RepoRoot:    repoRoot,
		Gen:         pool.Generation(drv.Name(), image, itype, disk, pool.SetupScript(repoRoot)),
		Size:        cfg.PoolSize(repo),
		MaxAge:      maxAge,
		Image:       image,
		IdleTimeout: idle, UnattendedCap: cap_,
		Manifest: cfg.Secrets.Manifest, SessionEnv: sessionEnv(cfg),
		Progress: func(step string) { fmt.Println(ui.Step(step)) },
	}, nil
}

// cmdPool: repo-scoped warm session pools — parked, setup-complete members
// that `pier <branch>` claims instead of creating. The TUI's w page is the
// primary surface; these commands are its scriptable equivalents.
func cmdPool(args []string) {
	if len(args) == 0 {
		poolStatus()
		return
	}
	switch args[0] {
	case "set":
		poolSet(args[1:])
	case "fill":
		poolFill(args[1:])
	case "drain":
		poolDrain(args[1:])
	default:
		fatal(fmt.Errorf("usage: pier pool [set <size> | fill [--detach] | drain [repo]]"))
	}
}

// poolStatus lists every pool the account holds — configured ones and
// leftovers alike — with what the parked members actually cost.
func poolStatus() {
	cfg, drv := loadDriver()
	sessions, err := drv.List(context.Background())
	if err != nil {
		fatal(err)
	}
	repos := map[string]bool{}
	for r := range cfg.Pool.Sizes {
		repos[r] = true
	}
	for _, s := range sessions {
		if s.PoolGen != "" {
			repos[s.Repo] = true
		}
	}
	if len(repos) == 0 {
		fmt.Println(ui.Dim.Render("no warm pools — `pier pool set <size>` from a repo keeps sessions ready to claim"))
		return
	}
	// Staleness is only computable for the repo we're standing in: the
	// generation hashes its local setup script.
	curRepo, curGen := "", ""
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(out))
		if pp, err := poolParams(cfg, drv, root); err == nil {
			curRepo, curGen = filepath.Base(root), pp.Gen
		}
	}
	maxAge, _ := cfg.PoolMaxAge() // unparsable → 0, which skips the age check
	names := make([]string, 0, len(repos))
	for r := range repos {
		names = append(names, r)
	}
	sort.Strings(names)
	now := time.Now()
	for _, repo := range names {
		st := tui.FoldPool(sessions, repo, curRepo, curGen, maxAge, now)
		line := fmt.Sprintf("%s: %d/%d warm", repo, st.Ready, cfg.PoolSize(repo))
		if len(st.Ages) > 0 {
			line += ui.Dim.Render(" (ages " + strings.Join(st.Ages, ", ") + ")")
		}
		if st.Filling > 0 {
			line += fmt.Sprintf(", %d filling", st.Filling)
		}
		if st.Stale > 0 {
			line += ui.Warn.Render(fmt.Sprintf(", %d stale", st.Stale)) + ui.Dim.Render(" (recycles on next claim/fill)")
		}
		if st.Ready > 0 {
			line += ui.Dim.Render(fmt.Sprintf(" — %d × ~$3-4/mo disk", st.Ready))
		}
		if cfg.PoolSize(repo) == 0 {
			line += ui.Warn.Render(" — no pool configured") + ui.Dim.Render(" — `pier pool drain "+repo+"` removes them")
		}
		fmt.Println(line)
	}
}

func poolSet(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier pool set <size>   (0-8; 0 turns the pool off — run from inside the repo)"))
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 || n > 8 {
		fatal(fmt.Errorf("pool size: want a number 0-8 (got %q)", args[0]))
	}
	cfg, drv := loadDriver()
	root := repoRoot()
	if err := applyPoolSize(cfg, drv, root, n, func(s string) { fmt.Println(ui.Step(s)) }); err != nil {
		fatal(err)
	}
	repo := filepath.Base(root)
	if n == 0 {
		fmt.Println(ui.OK.Render("pool off for " + repo))
		return
	}
	fmt.Println(ui.OK.Render(fmt.Sprintf("pool size %d for %s", n, repo)) +
		ui.Dim.Render(" — each warm member costs ~$3-4/mo parked (disk only)"))
	fmt.Println(ui.Dim.Render("filling in the background — log: " + ui.Tilde(poolLogPath(repo))))
}

// applyPoolSize is the one write path for a pool size, shared by the CLI and
// the TUI: saves the config, then drains (0) or starts a detached fill (>0).
func applyPoolSize(cfg config.Config, drv driver.Driver, repoRoot string, n int, progress func(string)) error {
	repo := filepath.Base(repoRoot)
	cfg.SetPoolSize(repo, n)
	if err := cfg.Save(); err != nil {
		return err
	}
	if n == 0 {
		sessions, err := drv.List(context.Background())
		if err != nil {
			return err
		}
		_, err = pool.Drain(context.Background(), drv, sessions, repo, progress)
		return err
	}
	_, err := spawnPoolFill(repoRoot)
	return err
}

func poolFill(args []string) {
	detach := len(args) > 0 && (args[0] == "--detach" || args[0] == "-d")
	cfg, drv := loadDriver()
	root := repoRoot()
	repo := filepath.Base(root)
	if cfg.PoolSize(repo) == 0 {
		fatal(fmt.Errorf("no pool configured for %s — `pier pool set <size>` first", repo))
	}
	if detach {
		if _, err := spawnPoolFill(root); err != nil {
			fatal(err)
		}
		fmt.Println(ui.Dim.Render("filling in the background — log: " + ui.Tilde(poolLogPath(repo))))
		return
	}
	pp, err := poolParams(cfg, drv, root)
	if err != nil {
		fatal(err)
	}
	// ctrl-c mid-fill must cancel the ctx (not just kill the process) so
	// Create's deferred cleanup can terminate a half-made member.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := pool.Fill(ctx, pp); err != nil {
		fatal(err)
	}
}

func poolDrain(args []string) {
	cfg, drv := loadDriver()
	var repo string
	if len(args) > 0 {
		repo = args[0]
	} else {
		repo = filepath.Base(repoRoot())
	}
	sessions, err := drv.List(context.Background())
	if err != nil {
		fatal(err)
	}
	count, err := pool.Drain(context.Background(), drv, sessions, repo, func(s string) { fmt.Println(ui.Step(s)) })
	if err != nil {
		fatal(err)
	}
	if count == 0 {
		fmt.Println(ui.Dim.Render("no warm members for " + repo))
		return
	}
	fmt.Println(ui.OK.Render(fmt.Sprintf("drained %d member(s)", count)))
	if n := cfg.PoolSize(repo); n > 0 {
		fmt.Println(ui.Dim.Render(fmt.Sprintf("pool size for %s is still %d — the next claim or fill re-warms it; `pier pool set 0` turns it off", repo, n)))
	}
}

func poolLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "pool-"+repo+".log")
}

// spawnPoolFill re-execs `pier pool fill` as a detached child working in
// repoRoot — the spawnCreate pattern — so a refill survives the invoking
// command (or the TUI) exiting.
func spawnPoolFill(repoRoot string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logPath := poolLogPath(filepath.Base(repoRoot))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return "", err
	}
	// Append, never truncate: a claim can spawn a refill while the previous
	// fill is still writing here, and clobbering a live writer's log turns
	// both runs into confetti. Fills are rare enough that growth is noise.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd := exec.Command(exe, "pool", "fill")
	cmd.Dir = repoRoot
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go cmd.Wait() // reap if this process outlives the fill
	return logPath, nil
}

// --- setup / doctor / bake / teardown ---------------------------------------------

func cmdSetup(args []string) {
	printAdmin := len(args) > 0 && args[0] == "--print-admin"
	if err := wizard.Run(newDriver, printAdmin); err != nil {
		fatal(err)
	}
}

func cmdDoctor() {
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		fmt.Println(ui.Warn.Render("!") + ui.Dim.Render(" no config yet — checking with defaults (run `pier setup`)"))
		cfg = config.Default()
	}
	drv, err := newDriver(cfg)
	if err != nil {
		fatal(err)
	}
	checks := drv.Doctor(context.Background())
	// Orphaned pool members are parked money nothing will ever claim: they
	// belong to repos whose pool was turned off (or another machine's config).
	// Members unparked past the fill grace are worse — a fill died before its
	// member parked, and the instance bills at full rate until something
	// notices; the next fill reconciles it away, but only doctor and the pool
	// pages will say so unprompted.
	if cfgErr == nil {
		if sessions, err := drv.List(context.Background()); err == nil {
			orphans, stuck := map[string]int{}, map[string]int{}
			for _, s := range sessions {
				switch {
				case s.PoolGen == "" || s.State == driver.StateDeleting:
				case s.State != driver.StateParked && s.State != driver.StateDead &&
					time.Since(s.Created) >= pool.FillGrace:
					stuck[s.Repo]++
				case cfg.PoolSize(s.Repo) == 0:
					orphans[s.Repo]++
				}
			}
			for repo, n := range orphans {
				checks = append(checks, driver.Check{
					Name:   "pool " + repo,
					Detail: fmt.Sprintf("%d warm member(s) but no pool configured — `pier pool drain %s`", n, repo),
				})
			}
			for repo, n := range stuck {
				checks = append(checks, driver.Check{
					Name:   "pool " + repo,
					Detail: fmt.Sprintf("%d member(s) stuck filling for 2h+ — running at full price; `pier pool fill` (from the repo) or `pier pool drain %s` recycles them", n, repo),
				})
			}
		}
	}
	if !printChecks(checks) {
		os.Exit(1)
	}
}

func printChecks(checks []driver.Check) bool {
	allOK := true
	for _, c := range checks {
		allOK = allOK && c.OK
		line := "  " + ui.Mark(c.OK) + " " + c.Name
		if c.Detail != "" {
			line += ui.Dim.Render(" — " + c.Detail)
		}
		fmt.Println(line)
	}
	return allOK
}

func cmdBake() {
	cfg, drv := loadDriver()
	repo := repoRoot() // images are repo-specific: bake from inside the repo it serves
	name := filepath.Base(repo)
	hook := driver.BakeHook(repo)
	fmt.Printf("%s %s\n", ui.Bold.Render("baking "+name),
		ui.Dim.Render("(one temporary instance ~5 min, then an image — ~$1-2/mo storage)"))
	if hook != "" {
		fmt.Println(ui.Step(".pier-bake.sh found — its toolchains bake in"))
	}
	// This bake supersedes the repo's previous image (and on aws-ec2, once
	// per config, the legacy shared one).
	replaces := cfg.BakedReplaces(name)
	// ctrl-c mid-bake must cancel the ctx (not just kill the process) so
	// Bake's deferred cleanup can terminate the temporary instance — it has
	// no supervisor, so a leaked one never parks itself.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	img, err := drv.Bake(ctx, driver.BakeSpec{
		RepoName: name, HookPath: hook, Replaces: replaces,
	})
	if err != nil {
		fatal(err)
	}
	cfg.RecordBake(name, img)
	if err := cfg.Save(); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render("baked "+img) + ui.Dim.Render(" — new "+name+" sessions now cold-start in ~1-2 min"))
	if cfg.PoolSize(name) > 0 {
		fmt.Println(ui.Dim.Render("the warm pool was built on the old image — members recycle on the next claim or `pier pool fill`"))
	}
}

func cmdTeardown() {
	cfg, drv := loadDriver()
	if !confirm("remove all pier groundwork and baked images from the account?", false) {
		return
	}
	// Pool members are invisible to `pier rm`, so teardown drains every pool
	// itself — Teardown refuses while instances exist, and rightly so.
	sessions, err := drv.List(context.Background())
	if err != nil {
		fatal(err)
	}
	repos := map[string]bool{}
	for _, s := range sessions {
		if s.PoolGen != "" {
			repos[s.Repo] = true
		}
	}
	for repo := range repos {
		if _, err := pool.Drain(context.Background(), drv, sessions, repo, func(s string) { fmt.Println(ui.Step(s)) }); err != nil {
			fatal(err)
		}
	}
	if err := drv.Teardown(context.Background()); err != nil {
		fatal(err)
	}
	cfg.ClearBakes()
	cfg.Pool.Sizes = nil
	cfg.Save()
	fmt.Println(ui.OK.Render("groundwork removed — the account is clean"))
}

func confirm(prompt string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s] ", prompt, hint)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// --- TUI --------------------------------------------------------------------------

func cmdTUI() {
	cfg, drv := loadDriver()
	// Pool sizing edits the repo pier launched from; outside a repo the w page
	// is read-only — a fill needs the local checkout and its setup script.
	// Inside a repo whose pool params don't resolve (a broken [pool] value,
	// say) the page is read-only too, but says why instead of pretending we
	// are not in a repo.
	curRoot, curRepo, curGen, poolNote := "", "", "", ""
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		curRoot = strings.TrimSpace(string(out))
		if pp, err := poolParams(cfg, drv, curRoot); err == nil {
			curRepo, curGen = filepath.Base(curRoot), pp.Gen
		} else {
			poolNote = "pool config unusable (" + err.Error() + ")"
		}
	}
	poolMaxAge, _ := cfg.PoolMaxAge() // unparsable → 0, which skips the age check
	err := tui.Run(tui.Options{
		// Async: the TUI opens instantly and the quota fills in when it lands.
		FetchQuota: func() string {
			q, err := drv.Headroom(context.Background())
			if err != nil {
				return ""
			}
			return q.Detail
		},
		Fetch: func() ([]driver.Session, error) {
			sessions, err := drv.List(context.Background())
			if err != nil {
				return nil, err
			}
			enrich(drv, sessions)
			return sessions, nil
		},
		Destroy: func(s driver.Session) error {
			return drv.Destroy(context.Background(), s.ID)
		},
		Pin: func(s driver.Session) error {
			return pin(drv, s)
		},
		Resize: func(s driver.Session, itype string) error {
			return drv.Resize(context.Background(), s.ID, itype)
		},
		Machines: func(s driver.Session) []driver.Machine {
			return drv.Machines(s.InstanceType)
		},
		// The settings machine picker serves whichever field is selected, so it
		// reads the catalog by driver id — not the active driver's — since a
		// user on AWS may still edit the gcp machine default.
		SettingsMachines: func(driverID, currentType string) []driver.Machine {
			if driverID == "gcp-gce" {
				return gcpgce.Machines(currentType)
			}
			return awsec2.Machines(currentType)
		},
		CreateDetached: spawnCreate,
		FetchLog: func(s driver.Session) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// The viewer refetches every few seconds, so the transfer stays
			// bounded; `pier logs` prints everything.
			out, err := drv.Exec(ctx, s.ID, `[ -f ~/.pier-setup.log ] && tail -n 2000 ~/.pier-setup.log || echo "no setup log — this session ran no setup script"`)
			if err == nil && strings.Count(out, "\n") >= 1999 {
				out = "… older lines trimmed — pier logs " + s.Name + " prints everything\n" + out
			}
			return out, err
		},
		Attach: func(s driver.Session) (*exec.Cmd, error) {
			return drv.AttachCommand(context.Background(), s.ID)
		},
		Resume: func(s driver.Session) error {
			return drv.Resume(context.Background(), s.ID)
		},
		RetryAttach: retryAttach,
		WaitReachable: func(s driver.Session) error {
			return waitReachable(drv, s.ID, 4*time.Minute)
		},
		CurrentRepo: curRepo,
		CurrentGen:  curGen,
		PoolMaxAge:  poolMaxAge,
		PoolNote:    poolNote,
		// Sizes reload from disk on each read so the w page sees a `pier pool
		// set` from another terminal.
		PoolSizes: func() map[string]int {
			c, err := config.Load()
			if err != nil {
				return cfg.Pool.Sizes
			}
			return c.Pool.Sizes
		},
		PoolSet: func(size int) (string, error) {
			c, err := config.Load()
			if err != nil {
				return "", err
			}
			if err := applyPoolSize(c, drv, curRoot, size, nil); err != nil {
				return "", err
			}
			if size == 0 {
				return "", nil
			}
			return poolLogPath(curRepo), nil
		},
		PoolFill: func() (string, error) {
			return spawnPoolFill(curRoot)
		},
		PoolDrain: func(repo string) (int, error) {
			sessions, err := drv.List(context.Background())
			if err != nil {
				return 0, err
			}
			return pool.Drain(context.Background(), drv, sessions, repo, nil)
		},
	})
	if err != nil {
		fatal(err)
	}
}

// spawnCreate re-execs `pier <branch> --detach` as a detached child (own
// session, output to a log file), so a TUI-initiated create runs in the
// background and survives the TUI closing. The list shows it as "creating"
// until the bootstrap finishes and the supervisor beacon appears.
func spawnCreate(branch string) (string, error) {
	if err := exec.Command("git", "rev-parse", "--show-toplevel").Run(); err != nil {
		return "", fmt.Errorf("not inside a git repository — run pier from the repo to fork")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(config.Dir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	logPath := filepath.Join(logDir, "create-"+strings.ReplaceAll(branch, "/", "-")+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd := exec.Command(exe, branch, "--detach")
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go cmd.Wait() // reap if the TUI outlives the create
	return logPath, nil
}

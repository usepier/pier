// pier — coding agent sessions as park-when-idle micro-VMs on your own cloud.
//
// This file is the CLI frontend: it parses flags, calls pkg/pier (the API
// every frontend shares), and renders the result. Behavior lives in the API;
// what's here is presentation.
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
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/proxy"
	"github.com/usepier/pier/internal/tombstone"
	"github.com/usepier/pier/internal/tui"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/internal/wizard"
	"github.com/usepier/pier/pkg/pier"
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

// options are the API options every CLI command runs with: the terminal
// gets transfer meters and transport notices.
func options() pier.Options {
	return pier.Options{
		SupervisorBin: supervisorBin,
		Version:       version,
		Out:           os.Stdout,
		Notify:        func(msg string) { fmt.Fprintln(os.Stderr, ui.Dim.Render("pier: "+msg)) },
	}
}

type helpItem struct {
	command     string
	description string
}

type helpSection struct {
	title string
	items []helpItem
}

var helpSections = []helpSection{
	{
		title: "Sessions",
		items: []helpItem{
			{"pier <branch> [base]", "new session off base (default HEAD), then attach"},
			{"  -d, --detach", "create without attaching"},
			{"  --idle <dur|never>", "park after this much idle time (default from settings)"},
			{"  --cap <dur|never>", "runaway cap for this session"},
			{"  --no-park", "never park this session when idle"},
			{"  --no-ready", "launch fresh instead of claiming a ready session"},
			{"pier ls [--json]", "list sessions"},
			{"pier attach <session>", "attach; a parked session resumes with its tmux restored"},
			{"pier logs <session> [-f]", "show or follow the setup log"},
			{"pier keep <session>", "never park this session when idle"},
			{"pier resize <session> <type>", "change the VM size (same CPU architecture)"},
			{"pier rm <session> [-f]", "destroy a session and its disk"},
		},
	},
	{
		title: "Repos",
		items: []helpItem{
			{"pier repos [--json]", "every repo: session image, ready sessions, monthly cost"},
			{"pier bake", "build this repo's session image (repo prebuilt; see settings)"},
			{"  --toolchain-only", "keep repo state out of the image for this bake"},
			{"  --with-repo", "prebuild the repo into the image for this bake"},
			{"pier ready [n]", "show, or set, how many ready sessions this repo keeps"},
		},
	},
	{
		title: "Access",
		items: []helpItem{
			{"pier proxy", "sessions as <session>.pier, ports mirrored to localhost (macOS)"},
			{"pier port <session> <port...>", "forward ports by hand (8080:3000 = local:session)"},
			{"pier mcp login <session> [server]", "one-time browser approval for OAuth MCP servers"},
		},
	},
	{
		title: "Setup",
		items: []helpItem{
			{"pier setup", "first-time setup (re-run any time)"},
			{"  --print-admin", "print the account setup for a cloud admin to run"},
			{"  --skills", "only install or refresh the bundled agent skills"},
			{"pier doctor", "check the environment and cloud account"},
			{"pier teardown", "remove all pier groundwork and images from the account"},
			{"pier version", "print the pier version"},
		},
	},
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		cmdTUI()
		return
	}
	switch args[0] {
	case "ls":
		cmdLS(args[1:])
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
	case "repos":
		cmdRepos(args[1:])
	case "bake":
		cmdBake(args[1:])
	case "ready":
		cmdReady(args[1:])
	case pier.RefillCommand:
		cmdRefill()
	case "setup":
		cmdSetup(args[1:])
	case "doctor":
		cmdDoctor()
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
	fmt.Println("\n " + ui.Title.Render("⚓ pier") +
		ui.Dim.Render(" — coding agent sessions as park-when-idle VMs on your own cloud") + "\n")
	command := fmt.Sprintf("%-35s", "pier")
	fmt.Printf("   %s  %s\n", ui.Accent.Render(command), "open the app: sessions, repos, settings")
	for _, section := range helpSections {
		fmt.Println()
		fmt.Println(" " + ui.Bold.Render(section.title))
		for _, item := range section.items {
			command = fmt.Sprintf("%-35s", item.command)
			fmt.Printf("   %s  %s\n", ui.Accent.Render(command), item.description)
		}
	}
	fmt.Println()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, ui.Bad.Render("pier:"), err)
	os.Exit(1)
}

func open() *pier.Client {
	c, err := pier.Open(options())
	if err != nil {
		fatal(err)
	}
	return c
}

func repoRoot() string {
	root, err := pier.RepoRoot("")
	if err != nil {
		fatal(err)
	}
	return root
}

// progress renders API events as the CLI's step lines, each with the time
// since the operation began.
func progress(e pier.Event) {
	elapsed := ui.Dim.Render(fmt.Sprintf("  +%s", e.Elapsed.Round(time.Second)))
	switch e.Kind {
	case pier.EventWarn:
		fmt.Println(ui.Warn.Render("  !") + " " + e.Message + elapsed)
	case pier.EventNote:
		fmt.Println(ui.Dim.Render("  · "+e.Message) + elapsed)
	default:
		fmt.Println(ui.Step(e.Message) + elapsed)
	}
}

func stepLine(msg string) { fmt.Println(ui.Step(msg)) }

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
	var detach, noPark, noReady bool
	fs.BoolVar(&detach, "d", false, "")
	fs.BoolVar(&detach, "detach", false, "")
	fs.BoolVar(&noPark, "no-park", false, "")
	fs.BoolVar(&noReady, "no-ready", false, "")
	idleS := fs.String("idle", "", "")
	capS := fs.String("cap", "", "")
	fs.Parse(flagArgs)
	if len(pos) < 1 || len(pos) > 2 {
		printUsage()
		os.Exit(1)
	}
	req := pier.CreateRequest{Branch: pos[0], NoReady: noReady, Progress: progress}
	if len(pos) == 2 {
		req.Base = pos[1]
	}
	if *idleS != "" || noPark {
		d, err := config.ParkDuration(*idleS)
		if err != nil {
			fatal(fmt.Errorf("--idle: %w", err))
		}
		if noPark {
			d = 0
		}
		req.Idle = &d
	}
	if *capS != "" {
		d, err := config.ParkDuration(*capS)
		if err != nil {
			fatal(fmt.Errorf("--cap: %w", err))
		}
		req.Cap = &d
	}

	c := open()
	req.RepoRoot = repoRoot()
	base := req.Base
	if base == "" {
		base = "HEAD"
	}
	fmt.Printf("%s %s\n", ui.Bold.Render("creating "+req.Branch),
		ui.Dim.Render(fmt.Sprintf("(%s @ %s)", filepath.Base(req.RepoRoot), base)))
	// ctrl-c mid-create must cancel the ctx (not just kill the process) so
	// the create's cleanup can terminate the half-made instance.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := c.Create(ctx, req)
	if err != nil {
		fatal(err)
	}
	stop() // create done — ctrl-c back to its default for the prompt + attach
	switch {
	case res.RefillErr != nil:
		fmt.Println(ui.Warn.Render("!") + ui.Dim.Render(" ready-session refill didn't start: "+res.RefillErr.Error()))
	case res.RefillLog != "":
		fmt.Println(ui.Dim.Render("refilling ready sessions in the background — log: " + ui.Tilde(res.RefillLog)))
	}
	sess := res.Session
	fmt.Println(ui.OK.Render("session " + sess.Name + " ready"))
	showReminders(c, req.RepoRoot)
	if detach {
		fmt.Println(ui.Dim.Render("attach with: pier attach " + sess.Name))
		return
	}
	// OAuth-backed MCPs need one browser approval each (tokens can't be
	// copied — they rotate); offer the sweep now, while a human is present.
	if names := pier.OAuthMCPServers(req.RepoRoot); len(names) > 0 && stdinIsTTY() {
		if confirm(fmt.Sprintf("mcp %s: run the one-time browser logins now?", strings.Join(names, ", ")), true) {
			loginAll(c, sess)
		} else {
			fmt.Println(ui.Dim.Render("later: pier mcp login " + sess.Name))
		}
	}
	attach(c, sess)
}

// showReminders prints rebake guidance after a create — at most once a day
// per repo, so it informs without nagging. The Repos tab always shows it.
func showReminders(c *pier.Client, repoRoot string) {
	rs := c.Reminders(repoRoot)
	if len(rs) == 0 || !remindDue(filepath.Base(repoRoot)) {
		return
	}
	for _, r := range rs {
		fmt.Println(ui.Accent.Render("  tip") + " " + r.Message + ui.Dim.Render(" `"+r.Action+"`"))
	}
}

// remindDue reports whether repo's reminders haven't been shown in the last
// day, and records that they're being shown now.
func remindDue(repo string) bool {
	path := filepath.Join(config.Dir(), "reminded.json")
	seen := map[string]time.Time{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	if time.Since(seen[repo]) < 24*time.Hour {
		return false
	}
	seen[repo] = time.Now()
	if b, err := json.Marshal(seen); err == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
	return true
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// attach runs the interactive ssh+tmux in the foreground. A fresh or
// just-resumed VM reports cloud-running before its transport answers, so an
// ssh attempt that dies instantly gets one bounded wait-for-reachability and
// a retry instead of a raw transport error dump.
func attach(c *pier.Client, s pier.Session) {
	fmt.Println(ui.Dim.Render("attaching — detach with C-b d (the session keeps running)"))
	retried := false
	for {
		cmd, err := c.AttachCommand(context.Background(), s)
		if err != nil {
			fatal(err)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		start := time.Now()
		runErr := cmd.Run()
		if runErr == nil {
			return
		}
		if retried || !pier.RetryAttach(runErr, time.Since(start)) {
			fmt.Fprintln(os.Stderr, ui.Bad.Render("pier:"), "attach:", runErr)
			return
		}
		retried = true
		fmt.Println(ui.Dim.Render("not reachable yet — waiting for the session to come online (fresh VMs take ~30-60s)"))
		if err := c.WaitReachable(context.Background(), s, 4*time.Minute); err != nil {
			fatal(err)
		}
	}
}

// --- sessions ---------------------------------------------------------------------

type sessionJSON struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Repository  string     `json:"repository"`
	Branch      string     `json:"branch"`
	Owner       string     `json:"owner"`
	Provider    string     `json:"provider"`
	State       pier.State `json:"state"`
	SetupState  string     `json:"setup_state"`
	Strained    bool       `json:"strained"`
	CreatedAt   *time.Time `json:"created_at"`
	MachineType string     `json:"machine_type"`
	CostNote    string     `json:"cost_note"`
}

func parseLSArgs(args []string) (bool, error) {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() != 0 {
		return false, fmt.Errorf("usage: pier ls [--json]")
	}
	return *jsonOutput, nil
}

func writeSessionsJSON(w io.Writer, sessions []pier.Session) error {
	items := make([]sessionJSON, 0, len(sessions))
	for _, s := range sessions {
		var createdAt *time.Time
		if !s.Created.IsZero() {
			created := s.Created.UTC()
			createdAt = &created
		}
		items = append(items, sessionJSON{
			ID: s.ID, Name: s.Name, Repository: s.Repo, Branch: s.Branch,
			Owner: s.User, Provider: s.Driver, State: s.State,
			SetupState: s.Setup, Strained: s.Strained, CreatedAt: createdAt,
			MachineType: s.InstanceType, CostNote: s.CostNote,
		})
	}
	return json.NewEncoder(w).Encode(items)
}

func cmdLS(args []string) {
	jsonOutput, err := parseLSArgs(args)
	if err != nil {
		fatal(err)
	}
	c := open()
	all, err := c.Sessions(context.Background())
	if err != nil {
		fatal(err)
	}
	// Ready sessions are inventory, not sessions: kept out of the JSON
	// entirely, and one dim summary line below the table instead of rows.
	sessions, ready := pier.SplitReady(all)
	readyLine := ""
	if len(ready) > 0 {
		readyLine = ui.Dim.Render(fmt.Sprintf("+ %d ready session(s) waiting — `pier repos`", len(ready)))
	}
	if jsonOutput {
		if err := writeSessionsJSON(os.Stdout, sessions); err != nil {
			fatal(err)
		}
		return
	}
	if len(sessions) == 0 {
		fmt.Println(ui.Dim.Render("no sessions — start one with `pier <branch>`"))
		if readyLine != "" {
			fmt.Println(readyLine)
		}
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tREPO\tSTATE\tAGE\tCOST")
	anyStrained, anySetupFailed, anyFailedCreate := false, false, false
	now := time.Now()
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Repo, stateLabel(s), pier.Age(s.Created, now), s.CostNote)
		anyStrained = anyStrained || s.Strained
		anySetupFailed = anySetupFailed || s.Setup == "failed"
		anyFailedCreate = anyFailedCreate || s.State == pier.StateFailed
	}
	w.Flush()
	if readyLine != "" {
		fmt.Println(readyLine)
	}
	if anyFailedCreate {
		fmt.Println("\n" + ui.Bad.Render("✗") + ui.Dim.Render(" create failed = the instance was rolled back, nothing is running — `pier rm <session>` clears the row"))
		for _, s := range sessions {
			if s.State != pier.StateFailed || s.FailReason == "" {
				continue
			}
			fmt.Println(ui.Dim.Render("  " + s.Name + ": " + tombstone.Summarize(s.FailReason)))
		}
	}
	if anyStrained {
		fmt.Println("\n" + ui.Warn.Render("!") + ui.Dim.Render(" strained = sustained cpu/mem pressure — grow with `pier resize <session> <type>`"))
	}
	if anySetupFailed {
		fmt.Println("\n" + ui.Warn.Render("!") + ui.Dim.Render(" setup failed = the setup script exited nonzero — `pier logs <session>` shows why"))
	}
}

// stateLabel renders the state plus the supervisor's strain and setup flags.
func stateLabel(s pier.Session) string {
	if s.State == pier.StateFailed {
		// "create failed", never a bare "failed": the neighbouring rows say
		// "setup failed" for a live session whose setup script died, and the
		// two mean very different things — one has a VM, this one does not.
		return "create failed"
	}
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

func match(c *pier.Client, query string) pier.Session {
	s, err := c.Match(context.Background(), query)
	if err != nil {
		fatal(err)
	}
	return s
}

func requireReady(s pier.Session) {
	if err := pier.CheckReady(s); err != nil {
		fatal(err)
	}
}

func resumeIfParked(c *pier.Client, s pier.Session) {
	if s.State == pier.StateParked {
		fmt.Println(ui.Dim.Render("resuming " + s.Name + " (~20-60s)..."))
		if err := c.Resume(context.Background(), s); err != nil {
			fatal(err)
		}
	}
}

func cmdAttach(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier attach <session>"))
	}
	c := open()
	s := match(c, args[0])
	requireReady(s)
	resumeIfParked(c, s)
	attach(c, s)
}

// cmdLogs prints the session's setup log without an attach — the first
// question after "(setup failed)" is always "what broke". -f follows a
// still-running setup live.
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
	c := open()
	s := match(c, rest[0])
	if s.State != pier.StateFailed {
		requireReady(s)
		if s.State == pier.StateParked {
			resumeIfParked(c, s)
			if err := c.WaitReachable(context.Background(), s, 4*time.Minute); err != nil {
				fatal(err)
			}
		}
	}
	if follow && s.State != pier.StateFailed {
		cmd, err := c.FollowLogCommand(context.Background(), s)
		if err != nil {
			fatal(err)
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run() // a followed log ends with ctrl-c; that interrupt is not an error
		return
	}
	out, err := c.SetupLog(context.Background(), s, 0)
	if err != nil {
		fatal(fmt.Errorf("logs: %w", err))
	}
	fmt.Println(out)
}

// cmdMCP: `pier mcp login <session> [server]` — one-time OAuth for MCP
// servers whose tokens live in the laptop's keychain and can't be copied
// (they rotate; two machines sharing one revoke each other). The callback
// port rides the session's tunnel, so the browser approval on the laptop
// completes the flow inside the VM. Without a server name it sweeps
// everything still unauthenticated.
func cmdMCP(args []string) {
	if len(args) < 2 || args[0] != "login" {
		fatal(fmt.Errorf("usage: pier mcp login <session> [server]"))
	}
	c := open()
	s := match(c, args[1])
	requireReady(s)
	resumeIfParked(c, s)
	if len(args) == 2 {
		loginAll(c, s)
		return
	}
	if err := loginOne(c, s, args[2]); err != nil {
		fatal(fmt.Errorf("mcp login: %w", err))
	}
}

// loginAll runs the browser flow for every OAuth-backed MCP server still
// lacking a token, sequentially — one command, N approvals, and re-running
// it is free: already-authenticated servers are skipped.
func loginAll(c *pier.Client, s pier.Session) {
	pending, err := c.PendingMCP(context.Background(), s)
	if err != nil {
		fatal(err)
	}
	if len(pending) == 0 {
		fmt.Println(ui.OK.Render("✓") + " every MCP server in " + s.Name + " is authenticated")
		return
	}
	fmt.Printf("%s %s\n", ui.Bold.Render(fmt.Sprintf("%d server(s) need a one-time browser approval:", len(pending))),
		strings.Join(pending, ", "))
	for i, server := range pending {
		fmt.Printf("\n%s %s\n", ui.Accent.Render(fmt.Sprintf("[%d/%d]", i+1, len(pending))), ui.Bold.Render(server))
		if err := loginOne(c, s, server); err != nil {
			fmt.Fprintln(os.Stderr, ui.Warn.Render("  ! "+server+" didn't finish — retry later with `pier mcp login "+s.Name+" "+server+"`"))
		}
	}
}

func loginOne(c *pier.Client, s pier.Session, server string) error {
	fmt.Println(ui.Dim.Render("  open the URL below and approve — the callback tunnels into the session; the token survives parking"))
	cmd, err := c.MCPLoginCommand(context.Background(), s, server)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// cmdProxy: `pier proxy` — every running session gets a hostname
// (<session>.pier) with its listening ports mirrored automatically; live
// connections keep the session from parking. Runs in the foreground until
// ctrl-c.
func cmdProxy() {
	// net/http logs transport chatter ("Unsolicited response received...")
	// through the global logger when probed ports answer oddly. All of the
	// proxy's real output goes through Options.Out, so drop the rest.
	log.SetOutput(io.Discard)
	c := open()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := proxy.Run(ctx, c.Driver(), proxy.Options{StateDir: config.Dir(), Out: os.Stdout}); err != nil {
		fatal(err)
	}
}

// cmdPort: `pier port <session> <port> [port...]` — hold ssh -L forwards open
// by hand. The zero-sudo, works-anywhere fallback to `pier proxy`.
func cmdPort(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: pier port <session> <port> [port...]   (3000, or local:session like 8080:3000)"))
	}
	c := open()
	s := match(c, args[0])
	var pairs [][2]int
	for _, a := range args[1:] {
		p, err := pier.ParsePortPair(a)
		if err != nil {
			fatal(err)
		}
		pairs = append(pairs, p)
	}
	resumeIfParked(c, s)
	for _, p := range pairs {
		fmt.Printf("  %s -> %s\n", ui.Bold.Render(fmt.Sprintf("localhost:%d", p[0])),
			ui.Dim.Render(fmt.Sprintf("%s:%d", s.Name, p[1])))
	}
	fmt.Println(ui.Dim.Render("  forwarding — ctrl-c to stop"))
	cmd, err := c.PortForwardCommand(context.Background(), s, pairs)
	if err != nil {
		fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("port forward: %w", err))
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
	c := open()
	s := match(c, names[0])
	if s.State != pier.StateFailed && !force && !confirm(fmt.Sprintf("destroy session %q and its disk?", s.Name), false) {
		return
	}
	if err := c.Remove(context.Background(), s); err != nil {
		fatal(err)
	}
	if s.State == pier.StateFailed {
		fmt.Println(ui.OK.Render("cleared failed create " + s.Name))
		return
	}
	fmt.Println(ui.OK.Render("destroyed " + s.Name))
}

func cmdKeep(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier keep <session>"))
	}
	c := open()
	s := match(c, args[0])
	if err := c.Keep(context.Background(), s); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render(s.Name+" pinned") + ui.Dim.Render(" — never parks when idle (the runaway cap still applies)"))
}

func cmdResize(args []string) {
	if len(args) != 2 {
		fatal(fmt.Errorf("usage: pier resize <session> <machine-type>"))
	}
	c := open()
	s := match(c, args[0])
	requireReady(s)
	itype := args[1]
	if s.State == pier.StateParked {
		fmt.Printf("%s %s\n", ui.Bold.Render("resizing "+s.Name+" to "+itype), ui.Dim.Render("(parked — stays parked)"))
	} else {
		fmt.Printf("%s %s\n", ui.Bold.Render("resizing "+s.Name+" to "+itype), ui.Dim.Render("(parks, resizes, resumes ~1-2 min)"))
	}
	if err := c.Resize(context.Background(), s, itype); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render(s.Name + " is now a " + itype))
}

// --- repos ------------------------------------------------------------------------

type repoJSON struct {
	Name        string          `json:"name"`
	Image       string          `json:"image"`
	BakedAt     *time.Time      `json:"baked_at"`
	ReadyTarget int             `json:"ready_target"`
	Ready       int             `json:"ready"`
	Filling     int             `json:"filling"`
	Sessions    int             `json:"sessions"`
	MonthlyUSD  float64         `json:"monthly_usd"`
	Reminders   []pier.Reminder `json:"reminders"`
}

func cmdRepos(args []string) {
	jsonOutput := len(args) > 0 && args[0] == "--json"
	c := open()
	sessions, err := c.Sessions(context.Background())
	if err != nil {
		fatal(err)
	}
	cur, _ := pier.RepoRoot("")
	repos := c.Repos(sessions, cur)
	if jsonOutput {
		items := make([]repoJSON, 0, len(repos))
		for _, r := range repos {
			j := repoJSON{Name: r.Name, Image: r.Image, ReadyTarget: r.ReadyTarget, Ready: r.Ready,
				Filling: r.Filling, Sessions: r.Sessions, MonthlyUSD: r.MonthlyUSD, Reminders: r.Reminders}
			if r.Baked != nil {
				t := r.Baked.BakedAt
				j.BakedAt = &t
			}
			items = append(items, j)
		}
		if err := json.NewEncoder(os.Stdout).Encode(items); err != nil {
			fatal(err)
		}
		return
	}
	if len(repos) == 0 {
		fmt.Println(ui.Dim.Render("no repos yet — `pier <branch>` inside one starts a session"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tIMAGE\tREADY\tSESSIONS\tIDLE COST")
	for _, r := range repos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.Name, imageLabel(r), readyLabel(r), r.Sessions, money(r.MonthlyUSD))
	}
	w.Flush()
	for _, r := range repos {
		for _, rem := range r.Reminders {
			fmt.Println(ui.Accent.Render("  tip") + " " + rem.Message + ui.Dim.Render(" `"+rem.Action+"`"))
		}
	}
}

func imageLabel(r pier.Repo) string {
	switch {
	case r.Image == "":
		return "none"
	case r.Baked == nil:
		return "baked"
	}
	kind := "prebuilt"
	if !r.Baked.RepoIncluded {
		kind = "toolchains"
	}
	return kind + ", " + pier.Age(r.Baked.BakedAt, time.Now()) + " old"
}

func readyLabel(r pier.Repo) string {
	if r.ReadyTarget == 0 && r.Ready == 0 && r.Filling == 0 {
		return "off"
	}
	l := fmt.Sprintf("%d/%d", r.Ready, r.ReadyTarget)
	if r.Filling > 0 {
		l += fmt.Sprintf(" (+%d filling)", r.Filling)
	}
	return l
}

func money(usd float64) string {
	if usd == 0 {
		return "$0"
	}
	return fmt.Sprintf("~$%.0f/mo", usd)
}

func cmdBake(args []string) {
	var include *bool
	for _, a := range args {
		switch a {
		case "--toolchain-only":
			f := false
			include = &f
		case "--with-repo":
			t := true
			include = &t
		default:
			fatal(fmt.Errorf("usage: pier bake [--toolchain-only | --with-repo]"))
		}
	}
	c := open()
	root := repoRoot() // images are repo-specific: bake from inside the repo it serves
	name := filepath.Base(root)
	withRepo := c.Config().Speed.ImageRepo
	if include != nil {
		withRepo = *include
	}
	what := "harnesses ~5 min, then one full .pier/setup.sh run"
	if !withRepo {
		what = "harnesses and .pier/bake.sh toolchains, ~5 min"
	}
	fmt.Printf("%s %s\n", ui.Bold.Render("baking "+name), ui.Dim.Render("("+what+" on a temporary instance, then an image — ~$1-3/mo storage)"))
	if withRepo {
		stepLine("prebuilding from HEAD — the checkout and setup's artifacts go into the image; every secret pier pushed is scrubbed first")
	}
	// ctrl-c mid-bake must cancel the ctx (not just kill the process) so
	// the bake's cleanup can terminate the temporary instance — it has no
	// supervisor, so a leaked one never parks itself.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := c.Bake(ctx, pier.BakeRequest{RepoRoot: root, IncludeRepo: include, Progress: progress})
	if err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render("baked "+res.Image) + ui.Dim.Render(" — new "+name+" sessions start from it; pier reminds you when a rebake would help"))
	if res.ReadyStale {
		fmt.Println(ui.Dim.Render("ready sessions were built on the old image — they recycle on the next claim or refill"))
	}
}

// cmdReady: `pier ready` shows this repo's ready sessions; `pier ready <n>`
// sets how many to keep (0 turns them off and removes the parked ones).
func cmdReady(args []string) {
	c := open()
	root := repoRoot()
	repo := filepath.Base(root)
	if len(args) == 0 {
		sessions, err := c.Sessions(context.Background())
		if err != nil {
			fatal(err)
		}
		for _, r := range c.Repos(sessions, root) {
			if r.Name != repo {
				continue
			}
			fmt.Printf("%s: %s ready sessions", repo, readyLabel(r))
			if r.Ready+r.Filling > 0 {
				fmt.Print(ui.Dim.Render(fmt.Sprintf(" — each ~$%.0f/mo parked (disk only)", c.DiskMonthlyUSD())))
			}
			fmt.Println()
			if r.Image == "" && r.ReadyTarget == 0 {
				fmt.Println(ui.Dim.Render("ready sessions follow the speed settings for repos with a session image — `pier bake` first"))
			}
		}
		return
	}
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier ready [n]   (0-8; 0 turns them off)"))
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 || n > 8 {
		fatal(fmt.Errorf("ready sessions: want a number 0-8 (got %q)", args[0]))
	}
	logPath, err := c.SetReady(context.Background(), root, n, func(e pier.Event) { stepLine(e.Message) })
	if err != nil {
		fatal(err)
	}
	if n == 0 {
		fmt.Println(ui.OK.Render("no ready sessions for " + repo))
		return
	}
	fmt.Println(ui.OK.Render(fmt.Sprintf("keeping %d ready session(s) for %s", n, repo)) +
		ui.Dim.Render(fmt.Sprintf(" — each ~$%.0f/mo parked (disk only)", c.DiskMonthlyUSD())))
	fmt.Println(ui.Dim.Render("filling in the background — log: " + ui.Tilde(logPath)))
}

// cmdRefill is the hidden command background refills re-exec.
func cmdRefill() {
	c := open()
	root := repoRoot()
	// ctrl-c must cancel the ctx so a half-made member is terminated.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.Refill(ctx, root, progress); err != nil {
		fatal(err)
	}
}

// --- setup / doctor / teardown ------------------------------------------------------

func cmdSetup(args []string) {
	printAdmin := false
	for _, a := range args {
		switch a {
		case "--print-admin":
			printAdmin = true
		case "--skills":
			cmdSkills()
			return
		default:
			fatal(fmt.Errorf("usage: pier setup [--print-admin | --skills]"))
		}
	}
	if err := wizard.Run(options(), printAdmin); err != nil {
		fatal(err)
	}
}

// cmdSkills refreshes the bundled skills for every agent on the machine,
// e.g. after a pier upgrade. Idempotent; a no-op prints as such.
func cmdSkills() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	agents := wizard.AgentDirs(home)
	if len(agents) == 0 {
		fmt.Println(ui.Dim.Render("  no agent config found (~/.claude or ~/.codex) — nothing to install into"))
		return
	}
	notes, err := wizard.InstallSkills(home, agents)
	for _, n := range notes {
		fmt.Println(n)
	}
	if err != nil {
		fatal(err)
	}
}

func cmdDoctor() {
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		fmt.Println(ui.Warn.Render("!") + ui.Dim.Render(" no config yet — checking with defaults (run `pier setup`)"))
		cfg = config.Default()
	}
	c, err := pier.New(cfg, options())
	if err != nil {
		fatal(err)
	}
	var checks []pier.Check
	if cfgErr == nil {
		checks = c.Doctor(context.Background())
	} else {
		checks = c.Driver().Doctor(context.Background())
	}
	if !printChecks(checks) {
		os.Exit(1)
	}
}

func printChecks(checks []pier.Check) bool {
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

func cmdTeardown() {
	c := open()
	if !confirm("remove all pier groundwork, images and ready sessions from the account?", false) {
		return
	}
	if err := c.Teardown(context.Background(), func(e pier.Event) { stepLine(e.Message) }); err != nil {
		fatal(err)
	}
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

// --- app ----------------------------------------------------------------------------

func cmdTUI() {
	root, _ := pier.RepoRoot("")
	// The app owns the screen: drivers must not write meters or notices into
	// it behind bubbletea's back.
	appOpts := options()
	appOpts.Out, appOpts.Notify = nil, nil
	err := tui.Run(tui.Options{
		Open: func() (tui.Backend, error) {
			c, err := pier.Open(appOpts)
			if err != nil {
				return nil, err // never a typed-nil *Client inside the interface
			}
			return c, nil
		},
		RepoRoot: root,
		Version:  version,
	})
	if errors.Is(err, tui.ErrNoConfig) {
		fmt.Println(ui.Dim.Render("pier isn't set up yet — run `pier setup`"))
		return
	}
	if err != nil {
		fatal(err)
	}
}

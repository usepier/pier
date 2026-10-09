package main

// Commands on sessions: new, ls, attach, logs, rm, keep, park, resize.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/tombstone"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func cmdNew(args []string) {
	// Split positionals from flags so `pier my-branch -d` works (Go's flag
	// package stops at the first positional).
	takesValue := map[string]bool{"-idle": true, "--idle": true, "-cap": true, "--cap": true,
		"-agent": true, "--agent": true, "-model": true, "--model": true, "-p": true, "--prompt": true,
		"-prompt-file": true, "--prompt-file": true, "-permission-mode": true, "--permission-mode": true,
		"-effort": true, "--effort": true}
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
	agentS := fs.String("agent", "", "")
	modelS := fs.String("model", "", "")
	effortS := fs.String("effort", "", "")
	permS := fs.String("permission-mode", "", "")
	var promptS, promptFile string
	fs.StringVar(&promptS, "p", "", "")
	fs.StringVar(&promptS, "prompt", "", "")
	fs.StringVar(&promptFile, "prompt-file", "", "") // a background create's prompt, kept out of argv
	fs.Parse(flagArgs)
	if promptFile != "" {
		b, err := os.ReadFile(promptFile)
		if err != nil {
			fatal(fmt.Errorf("--prompt-file: %w", err))
		}
		os.Remove(promptFile)
		promptS = string(b)
	}
	if len(pos) < 1 || len(pos) > 2 {
		printUsage()
		os.Exit(1)
	}
	req := pier.CreateRequest{Branch: pos[0], NoPool: noPool, Progress: progress}
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
	if *agentS != "" || promptS != "" {
		// Unset: the agent settings (pier settings → Agents) decide.
		req.Chat = &pier.ChatRequest{Agent: *agentS, Model: *modelS, Effort: *effortS, Mode: *permS}
		if promptS != "" {
			req.Chat.Prompt = &wire.ChatPrompt{Text: promptS}
		}
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
	case res.FillErr != nil:
		fmt.Println(ui.Warn.Render("!") + ui.Dim.Render(" pool refill didn't start: "+res.FillErr.Error()))
	case res.FillLog != "":
		fmt.Println(ui.Dim.Render("refilling the pool in the background — log: " + ui.Tilde(res.FillLog)))
	}
	sess := res.Session
	fmt.Println(ui.OK.Render("session " + sess.Name + " ready"))
	showReminders(c, req.RepoRoot)
	if detach {
		fmt.Println(ui.Dim.Render("attach with: pier attach " + sess.Name))
		return
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
		cmd, stopCompanion, err := c.Attach(context.Background(), s)
		if err != nil {
			fatal(err)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd = pier.ConnectingScreen(cmd, s.Name, true)
		start := time.Now()
		runErr := cmd.Run()
		stopCompanion()
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
			Origin: s.Origin, Base: s.Base, Owner: s.User, Provider: s.Driver, State: s.State,
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
	// Pooled sessions are inventory, not sessions: kept out of the JSON
	// entirely, and one dim summary line below the table instead of rows.
	sessions, pooled := pier.SplitPool(all)
	poolLine := ""
	if ready, filling := pier.PoolCounts(pooled); ready+filling > 0 {
		var parts []string
		if ready > 0 {
			parts = append(parts, fmt.Sprintf("%d pooled session(s) ready to claim", ready))
		}
		if filling > 0 {
			parts = append(parts, fmt.Sprintf("%d being set up", filling))
		}
		poolLine = ui.Dim.Render("+ " + strings.Join(parts, ", ") + " — `pier pool`")
	}
	if jsonOutput {
		if err := writeSessionsJSON(os.Stdout, sessions); err != nil {
			fatal(err)
		}
		return
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
	anyStrained, anySetupFailed, anyFailedCreate := false, false, false
	now := time.Now()
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Repo, stateLabel(s), pier.Age(s.Created, now), s.CostNote)
		anyStrained = anyStrained || s.Strained
		anySetupFailed = anySetupFailed || s.Setup == "failed"
		anyFailedCreate = anyFailedCreate || s.State == pier.StateFailed
	}
	w.Flush()
	if poolLine != "" {
		fmt.Println(poolLine)
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

// stateLabel renders the row's state plus the supervisor's strain and setup
// flags.
func stateLabel(s pier.Session) string {
	l := pier.Label(s)
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
	agent := false
	var pos []string
	for _, a := range args {
		if a == "--agent" || a == "-a" {
			agent = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		fatal(fmt.Errorf("usage: pier attach [--agent] <session>"))
	}
	c := open()
	s := match(c, pos[0])
	requireReady(s)
	resumeIfParked(c, s)
	if agent {
		// The session's chats, live, in a window of their own.
		if s.State == pier.StateParked {
			if err := c.WaitReachable(context.Background(), s, 4*time.Minute); err != nil {
				fatal(err)
			}
		}
		if _, err := c.ChatWindow(context.Background(), s, "", false); err != nil {
			fatal(err)
		}
	}
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

func cmdPark(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: pier park <session>"))
	}
	c := open()
	s := match(c, args[0])
	if s.State == pier.StateParked {
		fmt.Println(ui.Dim.Render(s.Name + " is already parked"))
		return
	}
	if err := c.Park(context.Background(), s); err != nil {
		fatal(err)
	}
	fmt.Println(ui.OK.Render(s.Name+" parking") + ui.Dim.Render(" — disk only from here; attach wakes it with its tmux windows back"))
}

// cmdDeliver wakes a session if it's parked and gives one of its chats a
// prompt: the background job a frontend starts for a parked session.
func cmdDeliver(args []string) {
	fs := flag.NewFlagSet("deliver", flag.ExitOnError)
	file := fs.String("file", "", "")
	var pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			fs.Parse(args[i:])
			break
		}
		pos = append(pos, args[i])
	}
	if len(pos) != 1 || *file == "" {
		fatal(fmt.Errorf("usage: pier deliver <session> --file <delivery.json>"))
	}
	d, err := pier.ReadDelivery(*file)
	if err != nil {
		fatal(err)
	}
	c := open()
	s := match(c, pos[0])
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.Deliver(ctx, s, d, progress); err != nil {
		fatal(err)
	}
	os.Remove(*file)
	fmt.Println(ui.OK.Render("prompt sent to " + s.Name))
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

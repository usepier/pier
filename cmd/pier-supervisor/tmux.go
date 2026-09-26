package main

// Restore-on-wake. Parking is a shutdown, so every process dies with it: the
// tmux server, the user's windows, an agent mid-conversation. The
// conversations themselves live on disk (claude and codex keep transcripts
// under $HOME) and survive; what a park loses is the arrangement around them.
// So the supervisor snapshots that arrangement while the session runs, and
// once more right before it parks, and `pier-supervisor restore` (run once
// per boot by pier-restore.service, before anyone attaches) rebuilds it: the
// same sessions, windows, layouts and cwds, each pane's scrollback replayed,
// agents relaunched on their conversation, anything else typed back at the
// prompt one Enter away. In-flight work (a running agent turn, a dev
// server's process) is gone for good: this brings back the desk, not the RAM.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	snapEvery       = 30 * time.Second
	scrollbackLines = 2000
	restoredMarker  = "/run/pier/restored"
	restoreUnitPath = "/etc/systemd/system/pier-restore.service"
)

// Pane kinds: what restore does with a pane.
const (
	kindClaude = "agent-claude" // relaunched on its conversation
	kindCodex  = "agent-codex"  // relaunched on its most recent conversation
	kindSetup  = "setup"        // plain shell with scrollback; setup never re-runs
	kindShell  = "shell"        // login shell in the saved cwd
	kindOther  = "other"        // shell with the old command typed, not run
)

type tmuxState struct {
	Saved    time.Time      `json:"saved"`
	Sessions []savedSession `json:"sessions"`
}

type savedSession struct {
	Name    string        `json:"name"`
	Windows []savedWindow `json:"windows"`
}

type savedWindow struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	// AutoName is tmux's automatic-rename: restore leaves such a window
	// unnamed so it keeps following its program instead of pinning whatever
	// the name happened to be at snapshot time.
	AutoName bool        `json:"auto_name,omitempty"`
	Active   bool        `json:"active,omitempty"`
	Layout   string      `json:"layout"`
	Panes    []savedPane `json:"panes"`
}

type savedPane struct {
	Index  int    `json:"index"`
	Active bool   `json:"active,omitempty"`
	Path   string `json:"path"`
	// Argv is the pane's foreground process as /proc recorded it.
	Argv []string `json:"argv,omitempty"`
	Kind string   `json:"kind"`
	// Resume is the claude conversation id when the command line named one.
	Resume string `json:"resume,omitempty"`
	// Command is what restore types back into an "other" pane.
	Command string `json:"command,omitempty"`
	// Scrollback names the pane's captured history file in the state dir.
	Scrollback string `json:"scrollback,omitempty"`

	id  string // tmux pane id (%N), only meaningful to the live server
	pid int
}

func stateDir(home string) string { return filepath.Join(home, ".pier", "tmux") }

// tmuxRunner runs one tmux command. The supervisor's talks to the agent's
// default server; the integration test points one at an isolated socket.
type tmuxRunner func(args ...string) ([]byte, error)

func defaultTmux(args ...string) ([]byte, error) {
	out, err := exec.Command("tmux", args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// --- snapshot -------------------------------------------------------------------

// paneFormat asks for every pane of every session in one list-panes call.
// The separator is printable on purpose: tmux 3.2 (Ubuntu 22.04) rewrites
// tabs and other control characters in format output as '_'. The cwd goes
// last, as the one free-form field likely to contain anything.
const paneSep = "|pier|"

var paneFormat = strings.Join([]string{
	"#{session_name}", "#{window_index}", "#{window_name}", "#{window_active}", "#{automatic-rename}",
	"#{window_layout}", "#{pane_index}", "#{pane_active}", "#{pane_id}", "#{pane_pid}", "#{pane_current_path}",
}, paneSep)

// parsePaneList folds list-panes output (in tmux's own session/window/pane
// order) into the saved tree.
func parsePaneList(out string) tmuxState {
	var st tmuxState
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, paneSep, 11)
		if len(f) != 11 {
			continue
		}
		wi, err1 := strconv.Atoi(f[1])
		pi, err2 := strconv.Atoi(f[6])
		pid, _ := strconv.Atoi(f[9])
		if err1 != nil || err2 != nil {
			continue
		}
		if n := len(st.Sessions); n == 0 || st.Sessions[n-1].Name != f[0] {
			st.Sessions = append(st.Sessions, savedSession{Name: f[0]})
		}
		s := &st.Sessions[len(st.Sessions)-1]
		if n := len(s.Windows); n == 0 || s.Windows[n-1].Index != wi {
			s.Windows = append(s.Windows, savedWindow{
				Index: wi, Name: f[2], Active: f[3] == "1", AutoName: f[4] == "1", Layout: f[5],
			})
		}
		w := &s.Windows[len(s.Windows)-1]
		w.Panes = append(w.Panes, savedPane{Index: pi, Active: f[7] == "1", id: f[8], pid: pid, Path: f[10]})
	}
	return st
}

// snapshotTmux records the live tmux layout into dir: state.json plus one
// scrollback file per pane, each written atomically so a hard stop mid-write
// can never leave restore a torn file. No server means nothing to record —
// the last snapshot stays, so a session that booted and was never attached
// can still be restored after its next park. fg maps a pane's pid to its
// foreground command line.
func snapshotTmux(run tmuxRunner, dir string, fg func(pid int) []string) error {
	out, err := run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return nil
	}
	st := parsePaneList(string(out))
	if len(st.Sessions) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keep := map[string]bool{}
	for si := range st.Sessions {
		s := &st.Sessions[si]
		for wi := range s.Windows {
			w := &s.Windows[wi]
			for pi := range w.Panes {
				p := &w.Panes[pi]
				p.Argv = fg(p.pid)
				p.Kind, p.Resume = classifyPane(w.Name, p.Argv)
				if p.Kind == kindOther {
					p.Command = commandLine(prettyArgv(p.Argv, exec.LookPath))
				}
				b, err := run("capture-pane", "-p", "-J", "-S", "-"+strconv.Itoa(scrollbackLines), "-t", p.id)
				if err != nil {
					continue
				}
				name := scrollbackName(s.Name, w.Index, p.Index)
				if writeIfChanged(filepath.Join(dir, name), trimTrailingBlank(b)) == nil {
					p.Scrollback = name
					keep[name] = true
				}
			}
		}
	}
	st.Saved = time.Now().UTC()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "state.json"), b); err != nil {
		return err
	}
	// Scrollback of panes that no longer exist would only pile up.
	old, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	for _, f := range old {
		if !keep[filepath.Base(f)] {
			os.Remove(f)
		}
	}
	return nil
}

// scrollbackName is the per-pane history file: <session>.<window>.<pane>.txt.
// tmux already forbids '.' and ':' in session names; '/' is the one character
// left that would escape the directory.
func scrollbackName(session string, window, pane int) string {
	return fmt.Sprintf("%s.%d.%d.txt", strings.ReplaceAll(session, "/", "_"), window, pane)
}

// trimTrailingBlank drops the empty rows below the cursor that capture-pane
// reports for the unused part of the screen — replayed, they would push the
// restored prompt to the bottom of the pane.
func trimTrailingBlank(b []byte) []byte {
	s := strings.TrimRight(string(b), " \t\r\n")
	if s == "" {
		return nil
	}
	return []byte(s + "\n")
}

// writeIfChanged skips identical rewrites: most panes are quiet between
// snapshots, and the disk is a network volume billed and throttled by IOPS.
func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

// foreground returns the command line of the process in the foreground of
// the pane whose root process is pid. The terminal's foreground process
// group (tpgid) is the program the user is looking at. When that group is
// the pane's shell itself, a child may still be running inside it: a program
// started from a startup file, before job control is up, shares the shell's
// group — so a child in the same group wins over the shell.
func foreground(pid int) []string {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil
	}
	_, tpgid, ok := parseStat(string(stat))
	if !ok || tpgid <= 0 {
		return procArgv(pid)
	}
	if tpgid != pid {
		if argv := procArgv(tpgid); argv != nil {
			return argv
		}
		return procArgv(pid)
	}
	kids, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	for _, k := range strings.Fields(string(kids)) {
		kid, _ := strconv.Atoi(k)
		ks, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", kid))
		if err != nil {
			continue
		}
		if pgrp, _, ok := parseStat(string(ks)); ok && pgrp == tpgid {
			return procArgv(kid)
		}
	}
	return procArgv(pid)
}

// parseStat pulls the process group and the terminal's foreground process
// group out of /proc/<pid>/stat. The command name in parentheses may itself
// contain spaces and parens, so fields are counted from the last ')'.
func parseStat(s string) (pgrp, tpgid int, ok bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	// state ppid pgrp session tty_nr tpgid ...
	if len(f) < 6 {
		return 0, 0, false
	}
	pgrp, err1 := strconv.Atoi(f[2])
	tpgid, err2 := strconv.Atoi(f[5])
	return pgrp, tpgid, err1 == nil && err2 == nil
}

func procArgv(pid int) []string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// --- classification -------------------------------------------------------------

var (
	shells       = map[string]bool{"bash": true, "sh": true, "dash": true, "zsh": true, "fish": true}
	interpreters = map[string]bool{"node": true, "nodejs": true, "python": true, "python3": true, "ruby": true, "perl": true}
	// Claude conversation ids are UUIDs. Anything else after --resume is a
	// search term for its picker, which a restore can't answer.
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// classifyPane decides what restore does with a pane, from its window name
// and foreground argv. For claude it also returns the conversation id the
// command line resumed or pinned, if any.
func classifyPane(window string, argv []string) (kind, resume string) {
	if window == "setup" || window == "setup-failed" || strings.Contains(strings.Join(argv, " "), ".pier-setup") {
		return kindSetup, ""
	}
	if len(argv) == 0 || isShell(argv) {
		return kindShell, ""
	}
	switch prog, args := program(argv); prog {
	case "claude":
		return kindClaude, claudeResumeID(args)
	case "codex":
		return kindCodex, ""
	}
	return kindOther, ""
}

// isShell reports an interactive shell at its prompt: a shell binary with
// only options (a login shell's argv[0] is "-bash"). `bash script.sh` or
// `bash -c ...` is running something, so it is not.
func isShell(argv []string) bool {
	if !shells[strings.TrimPrefix(filepath.Base(argv[0]), "-")] {
		return false
	}
	for i := 1; i < len(argv); i++ {
		switch a := argv[i]; {
		case a == "-c":
			return false
		case a == "--rcfile" || a == "--init-file":
			i++ // its value is a startup file, not a script
		case !strings.HasPrefix(a, "-"):
			return false
		}
	}
	return true
}

// program names what argv runs, looking through an interpreter: an npm
// install runs its CLIs as `node /usr/bin/codex ...` or
// `node .../@anthropic-ai/claude-code/cli.js ...`.
func program(argv []string) (name string, args []string) {
	base := filepath.Base(argv[0])
	if interpreters[base] && len(argv) > 1 && !strings.HasPrefix(argv[1], "-") {
		return scriptName(argv[1]), argv[2:]
	}
	return base, argv[1:]
}

func scriptName(path string) string {
	switch {
	case strings.Contains(path, "/claude-code/"):
		return "claude"
	case strings.Contains(path, "/@openai/codex/"):
		return "codex"
	}
	b := filepath.Base(path)
	for _, ext := range []string{".js", ".cjs", ".mjs", ".py"} {
		b = strings.TrimSuffix(b, ext)
	}
	return b
}

// claudeResumeID finds the conversation a claude command line resumed
// (--resume/-r) or pinned (--session-id), in either "--flag id" or
// "--flag=id" form. None means restore falls back to --continue.
func claudeResumeID(args []string) string {
	for i, a := range args {
		for _, flag := range []string{"--resume", "-r", "--session-id"} {
			v, ok := strings.CutPrefix(a, flag+"=")
			if !ok && a == flag && i+1 < len(args) {
				v, ok = args[i+1], true
			}
			if ok && uuidRe.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

// claudeKeptFlags carries over the flags that shape how a conversation runs
// rather than what it says. A restore that dropped
// --dangerously-skip-permissions would leave an unattended agent blocked on
// a permission prompt; a positional prompt must never ride along, or the
// relaunch would send it again.
func claudeKeptFlags(args []string) []string {
	var kept []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dangerously-skip-permissions":
			kept = append(kept, a)
		case a == "--model" || a == "--permission-mode":
			if i+1 < len(args) {
				kept = append(kept, a, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "--permission-mode="):
			kept = append(kept, a)
		}
	}
	return kept
}

// prettyArgv turns an interpreter-run CLI back into what the user typed:
// `node /usr/lib/node_modules/pnpm/bin/pnpm.cjs dev` becomes `pnpm dev`, as
// long as that name resolves on PATH to the same tool's entry point.
// Anything it can't map stays verbatim, which still reruns correctly.
func prettyArgv(argv []string, lookPath func(string) (string, error)) []string {
	if len(argv) < 2 || !interpreters[filepath.Base(argv[0])] || !filepath.IsAbs(argv[1]) {
		return argv
	}
	name := strings.TrimSuffix(scriptName(argv[1]), "-cli")
	if _, err := lookPath(name); err != nil {
		return argv
	}
	return append([]string{name}, argv[2:]...)
}

// launchCommand is what restore runs in an agent pane, relaunching the agent
// on its conversation. Other kinds run nothing.
func launchCommand(p savedPane) string {
	switch p.Kind {
	case kindClaude:
		var args []string
		if len(p.Argv) > 0 {
			_, args = program(p.Argv)
		}
		cmd := []string{"claude", "--continue"}
		if p.Resume != "" {
			cmd = []string{"claude", "--resume", p.Resume}
		}
		return commandLine(append(cmd, claudeKeptFlags(args)...))
	case kindCodex:
		return "codex resume --last"
	}
	return ""
}

// commandLine joins argv for a shell, quoting only the words that need it.
func commandLine(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shWord(a)
	}
	return strings.Join(q, " ")
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- restore plan ---------------------------------------------------------------

// planStep is one tmux command of a restore. The plan is plain data so the
// whole rebuild can be asserted on without a tmux binary.
type planStep struct {
	Args []string
	// Optional steps may fail harmlessly: moving a session's first window
	// onto the index it already has is an error to tmux, and which index
	// new-session picks depends on the user's base-index.
	Optional bool
	// AwaitPrompt holds the step until that pane's shell sits at its prompt.
	// Keys sent any earlier arrive while the tty still echoes on its own,
	// printing the command twice (once raw, once at the prompt).
	AwaitPrompt string
}

// rcBody is the startup file every restored pane's shell reads. A real login
// shell won't do: pier's ~/.bashrc ends by cd'ing into the repo, which would
// throw away the pane's saved cwd. So this reproduces one — /etc/profile and
// ~/.profile (PATH, then ~/.bashrc with ~/.config/pier/env) — and only then
// moves to the pane's cwd. The scrollback is replayed first so the restored
// prompt lands below it, tmux-resurrect style.
//
// The agent starts from the first prompt, via a one-shot PROMPT_COMMAND, not
// from this file: startup files run before bash turns job control on, so a
// program started here would share the shell's process group — tmux would
// see "bash" as the pane's command (and name the window after it), and
// Ctrl-Z would not reach the agent. It runs inside this interactive shell
// rather than replacing it, so quitting it leaves the user at a prompt in
// the same place instead of a closed pane.
const rcBody = `# Written by pier-supervisor restore; the startup file of a pane it rebuilt.
[ -n "${PIER_RESTORE_SCROLLBACK:-}" ] && cat -- "$PIER_RESTORE_SCROLLBACK" 2>/dev/null
[ -f /etc/profile ] && . /etc/profile
if [ -f ~/.profile ]; then . ~/.profile; elif [ -f ~/.bashrc ]; then . ~/.bashrc; fi
cd -- "${PIER_RESTORE_CWD:-$HOME}" 2>/dev/null
__pier_run=${PIER_RESTORE_RUN:-}
unset PIER_RESTORE_CWD PIER_RESTORE_SCROLLBACK PIER_RESTORE_RUN
if [ -n "$__pier_run" ]; then
  __pier_pc=${PROMPT_COMMAND:-}
  __pier_once() {
    PROMPT_COMMAND=$__pier_pc
    local run=$__pier_run
    unset -f __pier_once; unset __pier_run __pier_pc
    eval "$run"
  }
  PROMPT_COMMAND=__pier_once
else
  unset __pier_run
fi
`

// paneCommand is the shell-command tmux starts a restored pane with. The
// per-pane values ride env(1) on the command itself, not tmux -e: on
// new-session, -e sets the session environment, and every window the user
// opened later would replay this pane's scrollback and relaunch its agent.
func paneCommand(p savedPane, dir string) string {
	env := []string{"PIER_RESTORE_CWD=" + p.Path}
	if p.Scrollback != "" {
		env = append(env, "PIER_RESTORE_SCROLLBACK="+filepath.Join(dir, p.Scrollback))
	}
	if run := launchCommand(p); run != "" {
		env = append(env, "PIER_RESTORE_RUN="+run)
	}
	return "env " + commandLine(env) + " bash --rcfile " + shWord(filepath.Join(dir, "restore.rc")) + " -i"
}

// layoutSize reads the window size a layout string was captured at
// ("c3a1,203x51,0,0{...}").
func layoutSize(layout string) (w, h string, ok bool) {
	_, rest, found := strings.Cut(layout, ",")
	if !found {
		return "", "", false
	}
	dims, _, _ := strings.Cut(rest, ",")
	w, h, found = strings.Cut(dims, "x")
	if !found {
		return "", "", false
	}
	if _, err := strconv.Atoi(w); err != nil {
		return "", "", false
	}
	if _, err := strconv.Atoi(h); err != nil {
		return "", "", false
	}
	return w, h, true
}

// buildRestorePlan turns a snapshot into the tmux commands that rebuild it.
// `main` goes first — it is the session attach lands in, and it gets the
// SSH_AUTH_SOCK symlink the bootstrap's tmuxEnsure gives it (the other
// sessions get it too). Sessions are created at the size their layouts were
// captured at, so splits have room and select-layout reproduces the geometry
// before a client ever attaches. Panes are split off the newest pane, which
// keeps their indices in snapshot order for select-layout and targeting.
func buildRestorePlan(st tmuxState, home string) []planStep {
	dir := stateDir(home)
	sessions := make([]savedSession, 0, len(st.Sessions))
	for _, s := range st.Sessions {
		if s.Name == "main" {
			sessions = append([]savedSession{s}, sessions...)
		} else {
			sessions = append(sessions, s)
		}
	}
	var steps []planStep
	add := func(args ...string) { steps = append(steps, planStep{Args: args}) }
	for _, s := range sessions {
		var typed []planStep
		first, activeWin := true, ""
		for _, w := range s.Windows {
			if len(w.Panes) == 0 {
				continue
			}
			win := fmt.Sprintf("%s:%d", s.Name, w.Index)
			var name []string
			if !w.AutoName {
				name = []string{"-n", w.Name}
			}
			p0 := w.Panes[0]
			if first {
				args := []string{"new-session", "-d", "-s", s.Name}
				if x, y, ok := layoutSize(w.Layout); ok {
					args = append(args, "-x", x, "-y", y)
				}
				args = append(args, name...)
				args = append(args, "-c", p0.Path, "-e", "SSH_AUTH_SOCK="+home+"/.ssh/agent.sock", paneCommand(p0, dir))
				add(args...)
				steps = append(steps, planStep{Args: []string{"move-window", "-s", s.Name + ":^", "-t", win}, Optional: true})
				first = false
			} else {
				args := append([]string{"new-window", "-d", "-t", win}, name...)
				add(append(args, "-c", p0.Path, paneCommand(p0, dir))...)
			}
			activePane := ""
			for i, p := range w.Panes {
				if i > 0 {
					add("split-window", "-t", win, "-c", p.Path, paneCommand(p, dir))
				}
				target := fmt.Sprintf("%s.%d", win, p.Index)
				if p.Active {
					activePane = target
				}
				if p.Kind == kindOther && p.Command != "" {
					typed = append(typed, planStep{Args: []string{"send-keys", "-t", target, "-l", p.Command}, AwaitPrompt: target})
				}
			}
			if len(w.Panes) > 1 {
				add("select-layout", "-t", win, w.Layout)
				if activePane != "" {
					add("select-pane", "-t", activePane)
				}
			}
			if w.Active {
				activeWin = win
			}
		}
		if activeWin != "" {
			add("select-window", "-t", activeWin)
		}
		steps = append(steps, typed...)
	}
	return steps
}

// --- restore --------------------------------------------------------------------

// restoreMain is `pier-supervisor restore`, run once per boot by
// pier-restore.service. It always exits 0 and always leaves the marker
// attach waits on: a failed restore must degrade to today's fresh tmux, not
// to a failed unit or a hung attach.
func restoreMain() int {
	defer func() {
		_ = os.MkdirAll(filepath.Dir(restoredMarker), 0o755)
		_ = os.WriteFile(restoredMarker, nil, 0o644)
	}()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("pier-restore:", err)
		return 0
	}
	fmt.Println("pier-restore:", restoreTmux(defaultTmux, home))
	return 0
}

// restoreTmux rebuilds the saved layout unless a tmux server already runs
// (someone beat us to it; never stack a second layout onto theirs) or there
// is nothing saved. It returns the journal line describing what happened.
func restoreTmux(run tmuxRunner, home string) string {
	if _, err := run("list-sessions"); err == nil {
		return "tmux already running; leaving it alone"
	}
	dir := stateDir(home)
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "no saved layout; nothing to restore"
	}
	if err != nil {
		return "reading saved layout: " + err.Error()
	}
	var st tmuxState
	if err := json.Unmarshal(b, &st); err != nil {
		return "saved layout unreadable: " + err.Error()
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.rc"), []byte(rcBody), 0o644); err != nil {
		return "writing restore.rc: " + err.Error()
	}
	var failed []string
	for _, step := range buildRestorePlan(st, home) {
		if step.AwaitPrompt != "" {
			awaitPrompt(run, step.AwaitPrompt)
		}
		if _, err := run(step.Args...); err != nil && !step.Optional {
			failed = append(failed, fmt.Sprintf("tmux %s: %v", step.Args[0], err))
		}
	}
	msg := fmt.Sprintf("restored layout saved %s: %s", st.Saved.Format(time.RFC3339), summarize(st))
	if len(failed) > 0 {
		msg += fmt.Sprintf("; %d step(s) failed: %s", len(failed), strings.Join(failed, "; "))
	}
	return msg
}

func summarize(st tmuxState) string {
	var windows, panes int
	kinds := map[string]int{}
	for _, s := range st.Sessions {
		windows += len(s.Windows)
		for _, w := range s.Windows {
			panes += len(w.Panes)
			for _, p := range w.Panes {
				kinds[p.Kind]++
			}
		}
	}
	return fmt.Sprintf("%d session(s), %d window(s), %d pane(s) — %d claude, %d codex, %d command(s) typed back, %d shell(s), %d setup",
		len(st.Sessions), windows, panes, kinds[kindClaude], kinds[kindCodex], kinds[kindOther], kinds[kindShell], kinds[kindSetup])
}

// awaitPromptFor bounds the wait for a pane's shell; a var so tests don't
// sit it out on systems without GNU stty.
var awaitPromptFor = 10 * time.Second

// awaitPrompt waits until the pane's tty leaves canonical mode — readline
// switches it off exactly when bash starts reading a command line, so input
// sent from then on is drawn at the prompt instead of echoed raw. A timeout
// sends anyway: a doubled echo is cosmetic.
func awaitPrompt(run tmuxRunner, target string) {
	out, err := run("display-message", "-p", "-t", target, "#{pane_tty}")
	tty := strings.TrimSpace(string(out))
	if err != nil || tty == "" {
		return
	}
	for deadline := time.Now().Add(awaitPromptFor); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		o, err := exec.Command("stty", "-F", tty, "-a").Output()
		if err != nil {
			return
		}
		for _, f := range strings.Fields(string(o)) {
			if f == "-icanon" {
				return
			}
		}
	}
}

// --- supervisor hooks -----------------------------------------------------------

// snapshotNow is the supervisor's snapshot of the agent user's tmux.
func snapshotNow() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	if err := snapshotTmux(defaultTmux, stateDir(home), foreground); err != nil {
		fmt.Println("pier-supervisor: tmux snapshot:", err)
	}
}

// restoring reports a boot-time restore still in flight. A snapshot taken
// mid-rebuild would record a half-built layout over the full one it is
// rebuilding from.
func restoring() bool {
	if _, err := os.Stat(restoredMarker); err == nil {
		return false
	}
	if _, err := os.Stat(restoreUnitPath); err != nil {
		return false
	}
	out, _ := exec.Command("systemctl", "is-active", "pier-restore.service").Output()
	return strings.TrimSpace(string(out)) == "activating"
}

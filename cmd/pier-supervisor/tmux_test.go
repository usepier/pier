package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const convID = "0f8e2b7a-5c4d-4e3f-9a1b-2c3d4e5f6a7b"

func TestClassifyPane(t *testing.T) {
	cases := []struct {
		name       string
		window     string
		argv       []string
		kind, want string
	}{
		{"login shell", "bash", []string{"-bash"}, kindShell, ""},
		{"nested shell", "zsh", []string{"zsh", "-l"}, kindShell, ""},
		// The shell a restored pane runs must read as a shell next time.
		{"restored pane shell", "bash", []string{"bash", "--rcfile", "/home/agent/.pier/tmux/restore.rc", "-i"}, kindShell, ""},
		{"unknown process", "x", nil, kindShell, ""},
		{"bash running a script", "bash", []string{"bash", "deploy.sh"}, kindOther, ""},
		{"bash -c", "bash", []string{"bash", "-c", "make watch"}, kindOther, ""},
		{"plain claude", "claude", []string{"claude"}, kindClaude, ""},
		{"claude --resume id", "claude", []string{"claude", "--resume", convID}, kindClaude, convID},
		{"claude -r id", "claude", []string{"claude", "-r", convID, "--model", "opus"}, kindClaude, convID},
		{"claude --resume=id", "claude", []string{"claude", "--resume=" + convID}, kindClaude, convID},
		{"claude --session-id id", "claude", []string{"claude", "--dangerously-skip-permissions", "--session-id", convID}, kindClaude, convID},
		// A search term opens claude's picker; a restore can't answer it.
		{"claude --resume search term", "claude", []string{"claude", "--resume", "auth bug"}, kindClaude, ""},
		{"claude --resume then flag", "claude", []string{"claude", "--resume", "--model", "opus"}, kindClaude, ""},
		{"claude via npm", "node", []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "-r", convID}, kindClaude, convID},
		{"claude native path", "claude", []string{"/home/agent/.local/bin/claude", "--continue"}, kindClaude, ""},
		{"codex native", "codex", []string{"codex"}, kindCodex, ""},
		{"codex via npm shim", "node", []string{"node", "/usr/bin/codex", "resume", "--last"}, kindCodex, ""},
		{"codex via npm package", "node", []string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}, kindCodex, ""},
		{"dev server", "node", []string{"node", "/usr/lib/node_modules/pnpm/bin/pnpm.cjs", "dev"}, kindOther, ""},
		{"editor", "vim", []string{"vim", "main.go"}, kindOther, ""},
		{"setup window", "setup", []string{"bash", "-c", "whatever"}, kindSetup, ""},
		{"failed setup window", "setup-failed", []string{"sleep", "infinity"}, kindSetup, ""},
		{"setup by command", "bash", []string{"bash", "-c", "echo running > ~/.pier-setup.status; bash ./.pier/setup.sh"}, kindSetup, ""},
	}
	for _, c := range cases {
		kind, id := classifyPane(c.window, c.argv)
		if kind != c.kind || id != c.want {
			t.Errorf("%s: got (%s, %q), want (%s, %q)", c.name, kind, id, c.kind, c.want)
		}
	}
}

func TestLaunchCommand(t *testing.T) {
	cases := []struct {
		pane savedPane
		want string
	}{
		{savedPane{Kind: kindClaude, Argv: []string{"claude"}}, "claude --continue"},
		{savedPane{Kind: kindClaude, Argv: []string{"claude", "-r", convID}, Resume: convID}, "claude --resume " + convID},
		// Flags that shape how the agent runs survive; a positional prompt
		// must not, or the relaunch would send it again.
		{savedPane{Kind: kindClaude, Argv: []string{"claude", "--dangerously-skip-permissions", "--model", "opus", "fix the login bug"}},
			"claude --continue --dangerously-skip-permissions --model opus"},
		{savedPane{Kind: kindClaude, Argv: []string{"node", "/x/claude-code/cli.js", "--permission-mode=plan"}}, "claude --continue --permission-mode=plan"},
		{savedPane{Kind: kindCodex, Argv: []string{"codex", "do the thing"}}, "codex resume --last"},
		{savedPane{Kind: kindOther, Argv: []string{"pnpm", "dev"}}, ""},
		{savedPane{Kind: kindShell}, ""},
		{savedPane{Kind: kindSetup}, ""},
	}
	for _, c := range cases {
		if got := launchCommand(c.pane); got != c.want {
			t.Errorf("%v: got %q, want %q", c.pane.Argv, got, c.want)
		}
	}
}

func TestPrettyArgv(t *testing.T) {
	onPath := func(name string) (string, error) {
		if name == "pnpm" || name == "npm" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	cases := []struct{ in, want []string }{
		{[]string{"node", "/usr/lib/node_modules/pnpm/bin/pnpm.cjs", "dev"}, []string{"pnpm", "dev"}},
		{[]string{"node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "run", "dev"}, []string{"npm", "run", "dev"}},
		{[]string{"node", "server.js"}, []string{"node", "server.js"}},                                 // relative: the user typed it this way
		{[]string{"node", "/opt/tool/unknown.js", "x"}, []string{"node", "/opt/tool/unknown.js", "x"}}, // not on PATH
		{[]string{"make", "watch"}, []string{"make", "watch"}},
	}
	for _, c := range cases {
		if got := prettyArgv(c.in, onPath); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine([]string{"psql", "-c", "select 1", "it's", "a=b/c"})
	if want := `psql -c 'select 1' 'it'\''s' a=b/c`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestParseStat(t *testing.T) {
	// The comm field may hold spaces and parens; fields count from the last ')'.
	pgrp, tpgid, ok := parseStat("4242 (tmux: server) (x)) S 1 4242 4242 34816 5150 4194560 ...")
	if !ok || pgrp != 4242 || tpgid != 5150 {
		t.Errorf("got %d/%d/%v, want 4242/5150/true", pgrp, tpgid, ok)
	}
	if _, _, ok := parseStat("garbage"); ok {
		t.Error("garbage must not parse")
	}
}

func TestLayoutSize(t *testing.T) {
	if w, h, ok := layoutSize("b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}"); !ok || w != "200" || h != "50" {
		t.Errorf("got %s x %s (%v)", w, h, ok)
	}
	for _, bad := range []string{"", "b25f", "b25f,wide,0,0"} {
		if _, _, ok := layoutSize(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}

func TestParsePaneList(t *testing.T) {
	row := func(f ...string) string { return strings.Join(f, paneSep) + "\n" }
	out := row("main", "0", "editor", "1", "0", "b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}", "0", "1", "%0", "101", "/home/agent/work/shop") +
		row("main", "0", "editor", "1", "0", "b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}", "1", "0", "%1", "102", "/home/agent/work/shop/web") +
		row("main", "1", "bash", "0", "1", "aaaa,200x50,0,0,2", "0", "1", "%2", "103", "/home/agent/odd"+paneSep+"dir") +
		row("scratch", "0", "bash", "1", "1", "cccc,80x24,0,0,3", "0", "1", "%3", "104", "/home/agent")
	st := parsePaneList(out)
	if len(st.Sessions) != 2 || st.Sessions[0].Name != "main" || st.Sessions[1].Name != "scratch" {
		t.Fatalf("sessions: %+v", st.Sessions)
	}
	main := st.Sessions[0]
	if len(main.Windows) != 2 || len(main.Windows[0].Panes) != 2 || len(main.Windows[1].Panes) != 1 {
		t.Fatalf("main: %+v", main)
	}
	w0 := main.Windows[0]
	if w0.Name != "editor" || !w0.Active || w0.AutoName || w0.Layout != "b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}" {
		t.Errorf("window 0: %+v", w0)
	}
	if p := w0.Panes[1]; p.Index != 1 || p.Active || p.id != "%1" || p.pid != 102 || p.Path != "/home/agent/work/shop/web" {
		t.Errorf("pane 0.1: %+v", p)
	}
	if !main.Windows[1].AutoName {
		t.Error("automatic-rename must be recorded")
	}
	if p := main.Windows[1].Panes[0]; p.Path != "/home/agent/odd"+paneSep+"dir" {
		t.Errorf("the separator inside a cwd must survive: %q", p.Path)
	}
}

func TestBuildRestorePlan(t *testing.T) {
	st := tmuxState{Sessions: []savedSession{
		// Listed before main on purpose: main must still come back first.
		{Name: "scratch", Windows: []savedWindow{{
			Index: 1, Name: "codex", AutoName: true, Active: true, Layout: "cccc,80x24,0,0,3",
			Panes: []savedPane{{Index: 1, Active: true, Path: "/home/agent", Kind: kindCodex, Argv: []string{"codex"}}},
		}}},
		{Name: "main", Windows: []savedWindow{
			{
				Index: 0, Name: "editor", Active: true, Layout: "b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}",
				Panes: []savedPane{
					{Index: 0, Active: true, Path: "/home/agent/work/shop", Kind: kindClaude, Resume: convID,
						Argv: []string{"claude", "--resume", convID, "--dangerously-skip-permissions"}, Scrollback: "main.0.0.txt"},
					{Index: 1, Path: "/home/agent/work/shop/web", Kind: kindOther, Argv: []string{"pnpm", "dev"},
						Command: "pnpm dev", Scrollback: "main.0.1.txt"},
				},
			},
			{
				Index: 1, Name: "setup", Layout: "aaaa,200x50,0,0,2",
				Panes: []savedPane{{Index: 0, Active: true, Path: "/home/agent/work/shop", Kind: kindSetup,
					Argv: []string{"bash", "-c", "... .pier-setup.status ..."}, Scrollback: "main.1.0.txt"}},
			},
		}},
	}}
	const rc = " bash --rcfile /home/agent/.pier/tmux/restore.rc -i"
	claude := "env PIER_RESTORE_CWD=/home/agent/work/shop PIER_RESTORE_SCROLLBACK=/home/agent/.pier/tmux/main.0.0.txt 'PIER_RESTORE_RUN=claude --resume " + convID + " --dangerously-skip-permissions'" + rc
	// The dev server does not run on restore: it is typed at the prompt.
	dev := "env PIER_RESTORE_CWD=/home/agent/work/shop/web PIER_RESTORE_SCROLLBACK=/home/agent/.pier/tmux/main.0.1.txt" + rc
	// Setup never re-runs: a shell with its scrollback.
	setup := "env PIER_RESTORE_CWD=/home/agent/work/shop PIER_RESTORE_SCROLLBACK=/home/agent/.pier/tmux/main.1.0.txt" + rc
	codex := "env PIER_RESTORE_CWD=/home/agent 'PIER_RESTORE_RUN=codex resume --last'" + rc
	sock := "SSH_AUTH_SOCK=/home/agent/.ssh/agent.sock"

	want := []planStep{
		{Args: []string{"new-session", "-d", "-s", "main", "-x", "200", "-y", "50", "-n", "editor", "-c", "/home/agent/work/shop", "-e", sock, claude}},
		{Args: []string{"move-window", "-s", "main:^", "-t", "main:0"}, Optional: true},
		{Args: []string{"split-window", "-t", "main:0", "-c", "/home/agent/work/shop/web", dev}},
		{Args: []string{"select-layout", "-t", "main:0", "b25f,200x50,0,0{100x50,0,0,0,99x50,101,0,1}"}},
		{Args: []string{"select-pane", "-t", "main:0.0"}},
		{Args: []string{"new-window", "-d", "-t", "main:1", "-n", "setup", "-c", "/home/agent/work/shop", setup}},
		{Args: []string{"select-window", "-t", "main:0"}},
		{Args: []string{"send-keys", "-t", "main:0.1", "-l", "pnpm dev"}, AwaitPrompt: "main:0.1"},
		// automatic-rename windows stay unnamed so they keep following their program.
		{Args: []string{"new-session", "-d", "-s", "scratch", "-x", "80", "-y", "24", "-c", "/home/agent", "-e", sock, codex}},
		{Args: []string{"move-window", "-s", "scratch:^", "-t", "scratch:1"}, Optional: true},
		{Args: []string{"select-window", "-t", "scratch:1"}},
	}
	got := buildRestorePlan(st, "/home/agent")
	if !reflect.DeepEqual(got, want) {
		var b strings.Builder
		for _, s := range got {
			fmt.Fprintf(&b, "  %q optional=%v await=%q\n", s.Args, s.Optional, s.AwaitPrompt)
		}
		t.Errorf("plan mismatch; got:\n%s", b.String())
	}
}

// TestSnapshotLeavesLastStateWithoutServer: a boot that never started tmux
// must not wipe the layout the next wake restores from.
func TestSnapshotLeavesLastStateWithoutServer(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"sessions":[]}`), 0o600)
	noServer := func(args ...string) ([]byte, error) { return nil, errors.New("no server running") }
	if err := snapshotTmux(noServer, dir, func(int) []string { return nil }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state.json")); string(b) != `{"sessions":[]}` {
		t.Errorf("state was rewritten: %s", b)
	}
}

func TestRestoreSkipsRunningServerAndMissingState(t *testing.T) {
	home := t.TempDir()
	running := func(args ...string) ([]byte, error) {
		if args[0] != "list-sessions" {
			t.Errorf("restore touched a running server: %v", args)
		}
		return []byte("main: 1 windows\n"), nil
	}
	if msg := restoreTmux(running, home); !strings.Contains(msg, "already running") {
		t.Errorf("got %q", msg)
	}
	none := func(args ...string) ([]byte, error) {
		if args[0] != "list-sessions" {
			t.Errorf("restore ran tmux without a saved layout: %v", args)
		}
		return nil, errors.New("no server running")
	}
	if msg := restoreTmux(none, home); !strings.Contains(msg, "nothing to restore") {
		t.Errorf("got %q", msg)
	}
}

// TestSnapshotRestoreRoundTrip drives a real tmux on an isolated socket:
// snapshot a two-window session, kill the server (the park), restore, and
// check the layout, cwds and scrollback came back. Skipped without tmux.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	home := t.TempDir()
	work := filepath.Join(home, "work")
	os.MkdirAll(work, 0o755)
	sock := fmt.Sprintf("pier-test-%d", os.Getpid())
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("tmux", append([]string{"-L", sock, "-f", "/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home, "TMUX=")
		out, err := cmd.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%w: %s", err, ee.Stderr)
		}
		return out, err
	}
	t.Cleanup(func() { run("kill-server") })
	must := func(args ...string) {
		t.Helper()
		if out, err := run(args...); err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
	}
	awaitPromptFor = 3 * time.Second

	must("new-session", "-d", "-s", "main", "-x", "120", "-y", "40", "-n", "code", "-c", work, "bash --norc -i")
	must("send-keys", "-t", "main:0", "echo scrollback-marker", "Enter")
	must("new-window", "-d", "-t", "main:1", "-n", "server", "-c", home, "sleep 600")
	must("split-window", "-d", "-t", "main:1", "-c", work, "bash --norc -i")
	time.Sleep(500 * time.Millisecond)

	dir := stateDir(home)
	if err := snapshotTmux(run, dir, foreground); err != nil {
		t.Fatal(err)
	}
	var st tmuxState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &st)
	if len(st.Sessions) != 1 || len(st.Sessions[0].Windows) != 2 || len(st.Sessions[0].Windows[1].Panes) != 2 {
		t.Fatalf("snapshot: %s", b)
	}
	if runtime.GOOS == "linux" {
		if p := st.Sessions[0].Windows[1].Panes[0]; p.Kind != kindOther || p.Command != "sleep 600" {
			t.Errorf("sleep pane: kind %s command %q", p.Kind, p.Command)
		}
	}

	must("kill-server")
	msg := restoreTmux(run, home)
	if strings.Contains(msg, "failed") {
		t.Errorf("restore: %s", msg)
	}
	out, err := run("list-panes", "-a", "-F", "#{session_name}:#{window_index}:#{window_name}.#{pane_index} #{pane_current_path}")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main:0:code.0 " + work, "main:1:server.0 " + home, "main:1:server.1 " + work} {
		if !strings.Contains(string(out), want) {
			t.Errorf("restored panes missing %q:\n%s", want, out)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		shot, _ := run("capture-pane", "-p", "-t", "main:0.0")
		typed, _ := run("capture-pane", "-p", "-t", "main:1.0")
		scrollOK := strings.Contains(string(shot), "scrollback-marker")
		typedOK := runtime.GOOS != "linux" || strings.Contains(string(typed), "sleep 600")
		if scrollOK && typedOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scrollback replayed: %v, command typed back: %v\npane 0:\n%s\npane 1.0:\n%s", scrollOK, typedOK, shot, typed)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

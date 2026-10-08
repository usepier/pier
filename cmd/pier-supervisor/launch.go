package main

// Launching the agent and the hooks it reports through.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hooksPath is the settings file pier-launched claude reads its hooks from
// (--settings). It adds to the user's own settings and hooks, never
// replaces them.
const hooksPath = "/etc/pier/claude-hooks.json"

// hookEvents are the claude events pier listens to. Nothing per tool call:
// a hook there would add latency to every step the agent takes.
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Notification", "Stop", "SessionEnd", "PreCompact"}

func claudeHooks() []byte {
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		h := map[string]any{"type": "command", "command": "/usr/local/bin/pier-supervisor hook claude " + ev}
		if ev == "PermissionRequest" {
			// The request waits on the app for at most permissionWait; the
			// hook's own limit must never cut that short.
			h["timeout"] = 900
		}
		entry := map[string]any{"hooks": []any{h}}
		switch ev {
		case "PermissionRequest", "Notification", "PreCompact":
			entry["matcher"] = ""
		}
		hooks[ev] = []any{entry}
	}
	b, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	return b
}

// installHooksMain writes the hooks settings file: `pier-supervisor
// install-hooks`, run (with sudo) by every install of the supervisor.
func installHooksMain() int {
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor:", err)
		return 1
	}
	if err := writeAtomic(hooksPath, claudeHooks()); err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor:", err)
		return 1
	}
	os.Chmod(hooksPath, 0o644)
	return 0
}

// agentCommand is the command line that starts the agent a spec names.
func agentCommand(sp agentSpec) string {
	switch sp.Kind {
	case "codex":
		args := []string{"codex", "-c", `notify=["/usr/local/bin/pier-supervisor","hook","codex","notify"]`}
		if sp.Model != "" {
			args = append(args, "-m", sp.Model)
		}
		return commandLine(args)
	default:
		args := []string{"claude"}
		if uuidRe.MatchString(sp.Conversation) {
			args = append(args, "--session-id", sp.Conversation)
		}
		if sp.Model != "" {
			args = append(args, "--model", sp.Model)
		}
		if sp.PermissionMode != "" {
			args = append(args, "--permission-mode", sp.PermissionMode)
		}
		args = append(args, "--settings", hooksPath)
		return commandLine(args)
	}
}

// launchMain starts the agent the session was created with, once per
// launch spec: `pier-supervisor agent launch`, run at the end of a
// create's bootstrap and a claim's freshen. Its window carries
// @pier-role=agent, so frontends and restore know it.
func launchMain() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 1
	}
	dir := agentDir(home)
	b, err := os.ReadFile(filepath.Join(dir, "spec.json"))
	if err != nil {
		return 0 // no agent asked for
	}
	var sp agentSpec
	if err := json.Unmarshal(b, &sp); err != nil || sp.ID == "" {
		fmt.Fprintln(os.Stderr, "pier-supervisor: unreadable agent spec")
		return 1
	}
	if done, _ := os.ReadFile(filepath.Join(dir, "launched")); strings.TrimSpace(string(done)) == sp.ID {
		return 0
	}
	repo, _ := filepath.Glob(filepath.Join(home, "work", "*"))
	cwd := home
	if len(repo) > 0 {
		cwd = repo[0]
	}
	// The pane runs a shell whose first prompt starts the agent — restore's
	// way, so job control works and quitting the agent leaves a prompt.
	rc := filepath.Join(stateDir(home), "restore.rc")
	_ = os.MkdirAll(stateDir(home), 0o700)
	_ = writeIfChanged(rc, []byte(rcBody))
	cmd := paneCommand(savedPane{Path: cwd}, stateDir(home))
	cmd = strings.Replace(cmd, "env ", "env PIER_RESTORE_RUN="+shWord(agentCommand(sp))+" ", 1)
	if exec.Command("tmux", "has-session", "-t", "main").Run() != nil {
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", "main", "-c", cwd).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "pier-supervisor: tmux:", strings.TrimSpace(string(out)))
			return 1
		}
	}
	out, err := exec.Command("tmux", "new-window", "-d", "-t", "main:", "-n", "agent", "-P", "-F", "#{window_id}", "-c", cwd, cmd).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor: tmux new-window:", err)
		return 1
	}
	win := strings.TrimSpace(string(out))
	exec.Command("tmux", "set-option", "-w", "-t", win, "@pier-role", "agent").Run()
	exec.Command("tmux", "select-window", "-t", win).Run()
	_ = os.WriteFile(filepath.Join(dir, "launched"), []byte(sp.ID), 0o600)
	return 0
}

// hookMain is the hook handler agents run: `pier-supervisor hook <agent>
// <event>`, the event's JSON on stdin (codex's notify passes it as the
// last argument instead). It reports to the supervisor and, for a
// permission request, prints the frontend's decision — or nothing, which
// leaves the agent to ask in its terminal. It never fails the agent: any
// trouble reads as "no opinion".
func hookMain(args []string) int {
	if len(args) < 2 {
		return 0
	}
	kind, event := args[0], args[1]
	var raw []byte
	if kind == "codex" && len(args) > 2 {
		raw = []byte(args[len(args)-1])
	} else {
		raw, _ = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	}
	payload := map[string]any{}
	_ = json.Unmarshal(raw, &payload)
	pane := os.Getenv("TMUX_PANE")
	if event == "PermissionRequest" {
		req := map[string]any{"id": newOpID(), "op": "permission", "args": map[string]any{"pane": pane, "payload": payload}}
		b, _ := json.Marshal(req)
		out, code := opRequest(string(b))
		if code != 0 {
			return 0
		}
		var d decision
		if json.Unmarshal(out, &d) != nil || d.Behavior == "" {
			return 0
		}
		dec := map[string]any{"behavior": d.Behavior}
		if d.Message != "" {
			dec["message"] = d.Message
		}
		if len(d.UpdatedInput) > 0 {
			dec["updatedInput"] = d.UpdatedInput
		}
		resp, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": dec}})
		os.Stdout.Write(resp)
		return 0
	}
	req := map[string]any{"id": newOpID(), "op": "hook", "args": map[string]any{"kind": kind, "event": event, "pane": pane, "payload": payload}}
	b, _ := json.Marshal(req)
	opRequest(string(b))
	return 0
}

func newOpID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

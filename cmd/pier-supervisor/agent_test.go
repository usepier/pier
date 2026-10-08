package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentStateFollowsHooks(t *testing.T) {
	a := newAgents(t.TempDir(), nil)
	a.hook("claude", "SessionStart", "%3", map[string]any{"session_id": "s-1", "transcript_path": "/t.jsonl"})
	if p := a.primary(); p == nil || p.State != agentReady || p.Conversation != "s-1" || p.Transcript != "/t.jsonl" {
		t.Fatalf("after SessionStart: %+v", p)
	}
	a.hook("claude", "UserPromptSubmit", "%3", nil)
	if !a.turnOpen() {
		t.Error("a submitted prompt opens a turn: the session is busy")
	}
	a.hook("claude", "Notification", "%3", map[string]any{"notification_type": "permission_prompt"})
	if p := a.primary(); p.State != agentWaiting || a.turnOpen() {
		t.Errorf("waiting on the human is not busy: %+v", p)
	}
	a.hook("claude", "Stop", "%3", map[string]any{"last_assistant_message": "Done — tests pass."})
	if p := a.primary(); p.State != agentReady || p.Last != "Done — tests pass." {
		t.Errorf("after Stop: %+v", p)
	}
	a.hook("claude", "SessionEnd", "%3", nil)
	if a.primary() != nil {
		t.Error("an ended agent is not the primary one")
	}
}

// A woken session's agents start over: whatever ran is gone, and a turn
// that was mid-flight says so.
func TestAgentStateAfterABoot(t *testing.T) {
	home := t.TempDir()
	a := newAgents(home, nil)
	a.hook("claude", "UserPromptSubmit", "%1", map[string]any{"session_id": "s-1"})
	b := newAgents(home, nil)
	s := b.byPane["%1"]
	if s == nil || s.State != agentEnded || !strings.Contains(s.Last, "interrupted") || s.Conversation != "s-1" {
		t.Errorf("after a boot: %+v", s)
	}
}

func TestPermissionGating(t *testing.T) {
	a := newAgents(t.TempDir(), nil)
	// Nobody can answer: the agent asks in its terminal straight away.
	start := time.Now()
	if d := a.requestPermission("%1", "Bash", nil, func() bool { return false }); d.Behavior != "" || time.Since(start) > time.Second {
		t.Errorf("unanswerable request = %+v", d)
	}
	// Someone can: their answer comes back.
	done := make(chan decision)
	go func() {
		done <- a.requestPermission("%1", "Bash", json.RawMessage(`{"command":"ls"}`), func() bool { return true })
	}()
	var id string
	for range 100 {
		a.mu.Lock()
		if p := a.byPane["%1"]; p != nil && p.Pending != nil {
			id = p.Pending.ID
		}
		a.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !a.decide(id, decision{Behavior: "allow"}) {
		t.Fatal("the pending request must take a decision")
	}
	if d := <-done; d.Behavior != "allow" {
		t.Errorf("decision = %+v", d)
	}
	if a.decide(id, decision{Behavior: "deny"}) {
		t.Error("a decided request takes no second answer")
	}
	// Nobody answers in time: released to the terminal.
	old := permissionWait
	permissionWait = 50 * time.Millisecond
	defer func() { permissionWait = old }()
	if d := a.requestPermission("%1", "Bash", nil, func() bool { return true }); d.Behavior != "" {
		t.Errorf("a timed-out request is released, got %+v", d)
	}
}

func TestQueueOncePerID(t *testing.T) {
	home := t.TempDir()
	if err := enqueue(home, prompt{ID: "p1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	enqueue(home, prompt{ID: "p1", Text: "first again"})
	enqueue(home, prompt{ID: "p2", Text: "second", At: time.Now().Add(time.Second)})
	q := queued(home)
	if len(q) != 2 || q[0].Text != "first" {
		t.Fatalf("queue = %+v", q)
	}
	markSent(home, q[0])
	enqueue(home, prompt{ID: "p1", Text: "a replay after delivery"})
	if q := queued(home); len(q) != 1 || q[0].ID != "p2" {
		t.Errorf("a delivered prompt must not queue again: %+v", q)
	}
	if err := enqueue(home, prompt{ID: "../x", Text: "x"}); err == nil {
		t.Error("ids never name paths")
	}
	if _, err := os.Stat(filepath.Join(agentDir(home), "sent", "p1.json")); err != nil {
		t.Error("delivered prompts are kept as sent")
	}
}

func TestSanitize(t *testing.T) {
	in := "fix it\x1b[201~\x1b[200~rm -rf ~\r\nthen\ttest\x07"
	if got := sanitize(in); got != "fix it[201~[200~rm -rf ~\nthen\ttest" {
		t.Errorf("sanitize = %q", got)
	}
}

func TestHooksAndLaunch(t *testing.T) {
	var cfg struct {
		Hooks map[string][]struct {
			Matcher *string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(claudeHooks(), &cfg); err != nil {
		t.Fatal(err)
	}
	pr := cfg.Hooks["PermissionRequest"]
	if len(pr) != 1 || pr[0].Hooks[0].Timeout < 600 || !strings.HasSuffix(pr[0].Hooks[0].Command, "hook claude PermissionRequest") {
		t.Errorf("PermissionRequest hook = %+v", pr)
	}
	if _, ok := cfg.Hooks["PreToolUse"]; ok {
		t.Error("no per-tool hooks: they slow every step")
	}
	c := agentCommand(agentSpec{Kind: "claude", Model: "opus", Conversation: "11111111-2222-3333-4444-555555555555"})
	if c != "claude --session-id 11111111-2222-3333-4444-555555555555 --model opus --settings "+hooksPath {
		t.Errorf("claude launch = %q", c)
	}
	if c := agentCommand(agentSpec{Kind: "codex"}); !strings.Contains(c, `"hook","codex","notify"`) {
		t.Errorf("codex launch = %q", c)
	}
}

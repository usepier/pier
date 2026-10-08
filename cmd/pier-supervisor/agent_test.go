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

// A supervisor restart (an upgrade) keeps the agents: they still run. A
// request that was waiting went back to the agent's terminal.
func TestAgentStateAfterARestart(t *testing.T) {
	old := bootID
	bootID = func() string { return "boot-1" }
	t.Cleanup(func() { bootID = old })
	home := t.TempDir()
	a := newAgents(home, nil)
	a.hook("claude", "UserPromptSubmit", "%1", map[string]any{"session_id": "s-1"})
	a.mu.Lock()
	a.byPane["%1"].Pending, a.byPane["%1"].State, a.byPane["%1"].Waiting = &pending{ID: "p1", Tool: "Bash"}, agentWaiting, "permission"
	a.mu.Unlock()
	a.changed()
	b := newAgents(home, nil)
	s := b.byPane["%1"]
	if s == nil || s.State != agentWaiting || s.Pending != nil || s.Waiting != "input" {
		t.Errorf("after a restart: %+v", s)
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

// A question's answers go back in the tool's input; an approved plan can
// switch the agent to accepting edits; a refusal carries its reason.
func TestAnswer(t *testing.T) {
	in := json.RawMessage(`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`)
	d, err := answer(in, "allow", "", map[string]string{"Which database?": "Postgres"}, map[string]string{"Which database?": "we run it already"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(d.UpdatedInput, &got)
	if got["answers"].(map[string]any)["Which database?"] != "Postgres" || got["questions"] == nil ||
		got["annotations"].(map[string]any)["Which database?"].(map[string]any)["notes"] != "we run it already" {
		t.Errorf("updated input = %s", d.UpdatedInput)
	}
	d, _ = answer(json.RawMessage(`{"plan":"1. do it"}`), "allow", "", nil, nil, "acceptEdits")
	if len(d.UpdatedInput) != 0 || string(d.UpdatedPermissions) != `[{"destination":"session","mode":"acceptEdits","type":"setMode"}]` {
		t.Errorf("plan approval = %+v", d)
	}
	d, _ = answer(nil, "deny", "split step 2", nil, nil, "acceptEdits")
	if d.Behavior != "deny" || d.Message != "split step 2" || d.UpdatedPermissions != nil {
		t.Errorf("keep planning = %+v", d)
	}
	if _, err := answer(nil, "allow", "", nil, nil, "yolo"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestWithPlan(t *testing.T) {
	home := t.TempDir()
	plan := filepath.Join(home, ".claude", "plans", "p.md")
	os.MkdirAll(filepath.Dir(plan), 0o700)
	os.WriteFile(plan, []byte("# Plan\n1. fix it"), 0o600)
	in := map[string]any{"planFilePath": plan}
	withPlan(home, "ExitPlanMode", in)
	if in["plan"] != "# Plan\n1. fix it" {
		t.Errorf("plan = %v", in["plan"])
	}
	out := map[string]any{"planFilePath": "/etc/passwd"}
	withPlan(home, "ExitPlanMode", out)
	if out["plan"] != nil {
		t.Error("a file outside home was read")
	}
}

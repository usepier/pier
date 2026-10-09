package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// testHost is a chat host whose agents are the fake (fakeagent_test.go)
// replaying a recording; heard is what the fake heard from the host.
func testHost(t *testing.T, kind, fixture string) (h *chatHost, heard string) {
	t.Helper()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "work", "repo"), 0o755)
	heard = filepath.Join(t.TempDir(), "heard.jsonl")
	t.Setenv(fakeEnv, kind+":"+fixturePath(fixture))
	t.Setenv(fakeHeardEnv, heard)
	h = newChatHost(home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h.program = func(string) (string, string) { return exe, "" }
	t.Cleanup(func() {
		h.mu.Lock()
		var procs []agentProc
		for _, c := range h.chats {
			if c.rt != nil {
				procs = append(procs, c.rt)
			}
		}
		codex := h.codex
		h.mu.Unlock()
		for _, p := range procs {
			p.stop()
		}
		if codex != nil && codex.alive() {
			codex.mu.Lock()
			cmd := codex.cmd
			codex.mu.Unlock()
			killGroup(cmd)
		}
	})
	return h, heard
}

func chatInfo(h *chatHost, id string) (wire.ChatInfo, bool) {
	for _, c := range h.topic()["chats"].([]wire.ChatInfo) {
		if c.ID == id {
			return c, true
		}
	}
	return wire.ChatInfo{}, false
}

// waitChat waits for a chat to get where ok says.
func waitChat(t *testing.T, h *chatHost, id, what string, ok func(wire.ChatInfo) bool) wire.ChatInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h.deliverAll()
		if c, found := chatInfo(h, id); found && ok(c) {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, _ := chatInfo(h, id)
	items, _ := chatItems(h, id)
	t.Fatalf("chat %s never got %s: %+v\nitems: %+v", id, what, c, items)
	return c
}

func chatItems(h *chatHost, id string) ([]wire.ChatItem, error) {
	l := h.logOf(id)
	items, _, err := readItems(l.path())
	return items, err
}

func pendingKind(kind string) func(wire.ChatInfo) bool {
	return func(c wire.ChatInfo) bool { return c.Pending != nil && c.Pending.Kind == kind }
}

func ready(c wire.ChatInfo) bool { return c.State == wire.AgentReady }

// heardFrom is what the host told the fake, one JSON object per message.
func heardFrom(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestClaudeChatTurnWithApprovals(t *testing.T) {
	h, heard := testHost(t, "claude", "claude-tools.jsonl")
	if err := h.create(createArgs{ID: "c1", Agent: "claude", Mode: "default", Prompt: &prompt{ID: "p1", Text: "Create hello.txt, run ls, edit math.py"}}); err != nil {
		t.Fatal(err)
	}
	c := waitChat(t, h, "c1", "a Write approval", pendingKind("tool"))
	if c.State != wire.AgentWaiting || c.Waiting != "permission" || c.Pending.Tool != "Write" || c.Pending.Call == nil ||
		c.Pending.Call.View != wire.ToolWrite || !c.Pending.AllowSession || c.Title != "Create hello.txt, run ls, edit math.py" {
		t.Fatalf("waiting = %+v %+v", c, c.Pending)
	}
	// While it waits, a second prompt queues behind the turn.
	if err := h.send("c1", prompt{ID: "p2", Text: "and then?"}, "queue"); err != nil {
		t.Fatal(err)
	}
	if c, _ = chatInfo(h, "c1"); len(c.Queue) != 1 || c.Queue[0].ID != "p2" {
		t.Fatalf("queue = %+v", c.Queue)
	}
	if err := h.decide("c1", c.Pending.ID, decision{Behavior: "allowSession"}); err != nil {
		t.Fatal(err)
	}
	c = waitChat(t, h, "c1", "an Edit approval", func(c wire.ChatInfo) bool { return c.Pending != nil && c.Pending.Tool == "Edit" })
	if c.Pending.Call.Edit == nil || len(c.Pending.Call.Edit.Hunks) == 0 {
		t.Errorf("the edit card shows the change: %+v", c.Pending.Call)
	}
	if err := h.decide("c1", c.Pending.ID, decision{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if err := h.decide("c1", c.Pending.ID, decision{Behavior: "allow"}); err == nil {
		t.Error("a request answered twice")
	}
	// The turn ends; the queued prompt goes in on its own.
	c = waitChat(t, h, "c1", "the queued prompt delivered", func(c wire.ChatInfo) bool { return len(c.Queue) == 0 && c.State == wire.AgentWorking })
	items, _ := chatItems(h, "c1")
	var ids []string
	for _, it := range items {
		if it.Kind == wire.ChatUser || it.Kind == wire.ChatAssistant {
			ids = append(ids, string(it.Kind)+":"+it.Text)
		}
	}
	want := "user:Create hello.txt, run ls, edit math.py|assistant:I'll complete these tasks in sequence.|assistant:done|user:and then?"
	if strings.Join(ids, "|") != want {
		t.Errorf("conversation = %s", strings.Join(ids, "|"))
	}
	for _, it := range items {
		if it.Tool != nil && it.Tool.Status != wire.ToolOK {
			t.Errorf("tool %s ended %s", it.Tool.Name, it.Tool.Status)
		}
	}
	if c.Last != "done" {
		t.Errorf("last = %q", c.Last)
	}
	// What the host told claude: the handshake, the prompt, the answers.
	var allowSession, user bool
	for _, m := range heardFrom(t, heard) {
		if m["type"] == "user" {
			user = true
		}
		if r, _ := m["response"].(map[string]any); r != nil {
			if resp, _ := r["response"].(map[string]any); resp != nil && resp["updatedPermissions"] != nil {
				allowSession = true
			}
		}
	}
	if !user || !allowSession {
		t.Errorf("heard: user %v, allow for the session %v", user, allowSession)
	}
	// A prompt id already delivered is a no-op.
	if err := h.send("c1", prompt{ID: "p1", Text: "again"}, "queue"); err != nil {
		t.Fatal(err)
	}
	if c, _ = chatInfo(h, "c1"); len(c.Queue) != 0 {
		t.Error("a delivered prompt id was queued again")
	}
}

func TestClaudeChatQuestion(t *testing.T) {
	h, heard := testHost(t, "claude", "claude-question.jsonl")
	h.create(createArgs{ID: "q", Agent: "claude", Prompt: &prompt{ID: "p1", Text: "ask me"}})
	c := waitChat(t, h, "q", "the questions", pendingKind("question"))
	if c.Waiting != "question" || len(c.Pending.Questions) != 2 || c.Pending.Questions[1].MultiSelect != true {
		t.Fatalf("pending = %+v", c.Pending)
	}
	if err := h.decide("q", c.Pending.ID, decision{Behavior: "allow"}); err == nil {
		t.Error("answering questions with no answers")
	}
	c = waitChat(t, h, "q", "the questions again", pendingKind("question"))
	answers := map[string]string{"Which color do you prefer?": "Blue", "Which fruits do you like?": "Apple, Banana"}
	if err := h.decide("q", c.Pending.ID, decision{Behavior: "allow", Answers: answers, Notes: map[string]string{"Which color do you prefer?": "a note"}}); err != nil {
		t.Fatal(err)
	}
	waitChat(t, h, "q", "the end of the turn", ready)
	var sent map[string]any
	for _, m := range heardFrom(t, heard) {
		if r, _ := m["response"].(map[string]any); r != nil {
			resp, _ := r["response"].(map[string]any)
			if in, _ := resp["updatedInput"].(map[string]any); in != nil {
				sent = in
			}
		}
	}
	if sent == nil || sent["answers"].(map[string]any)["Which fruits do you like?"] != "Apple, Banana" || sent["annotations"] == nil || sent["questions"] == nil {
		t.Errorf("answers sent = %+v", sent)
	}
}

func TestClaudeChatPlan(t *testing.T) {
	h, heard := testHost(t, "claude", "claude-plan.jsonl")
	h.create(createArgs{ID: "pl", Agent: "claude", Mode: "plan", Prompt: &prompt{ID: "p1", Text: "plan it"}})
	c := waitChat(t, h, "pl", "a plan", pendingKind("plan"))
	if !strings.Contains(c.Pending.Plan, "add(a, b)") {
		t.Fatalf("plan = %q", c.Pending.Plan)
	}
	h.decide("pl", c.Pending.ID, decision{Behavior: "deny", Message: "Also give the function a docstring."})
	c = waitChat(t, h, "pl", "the revised plan", func(c wire.ChatInfo) bool {
		return c.Pending != nil && c.Pending.Kind == "plan" && strings.Contains(c.Pending.Plan, "docstring")
	})
	if err := h.decide("pl", c.Pending.ID, decision{Behavior: "allow", Mode: "plan"}); err == nil {
		t.Error("approving a plan into plan mode")
	}
	c = waitChat(t, h, "pl", "the plan still waiting", pendingKind("plan"))
	h.decide("pl", c.Pending.ID, decision{Behavior: "allow", Mode: "acceptEdits"})
	c = waitChat(t, h, "pl", "the Bash approval", pendingKind("tool"))
	if c.Mode != "acceptEdits" {
		t.Errorf("mode after approving = %q", c.Mode)
	}
	h.decide("pl", c.Pending.ID, decision{Behavior: "allow"})
	waitChat(t, h, "pl", "the end of the turn", ready)
	var denied, setMode bool
	for _, m := range heardFrom(t, heard) {
		r, _ := m["response"].(map[string]any)
		resp, _ := r["response"].(map[string]any)
		if resp["behavior"] == "deny" && resp["message"] == "Also give the function a docstring." {
			denied = true
		}
		if ps, _ := resp["updatedPermissions"].([]any); len(ps) == 1 && ps[0].(map[string]any)["mode"] == "acceptEdits" {
			setMode = true
		}
	}
	if !denied || !setMode {
		t.Errorf("heard: feedback %v, mode switch %v", denied, setMode)
	}
}

func TestClaudeChatSteerAndInterrupt(t *testing.T) {
	h, heard := testHost(t, "claude", "claude-interrupt.jsonl")
	h.create(createArgs{ID: "i", Agent: "claude", Mode: "bypassPermissions", Prompt: &prompt{ID: "p1", Text: "sleep"}})
	waitChat(t, h, "i", "a running command", func(c wire.ChatInfo) bool {
		items, _ := chatItems(h, "i")
		for _, it := range items {
			if it.Tool != nil && it.Tool.View == wire.ToolBash {
				return true
			}
		}
		return false
	})
	if err := h.send("i", prompt{ID: "p2", Text: "Actually, also tell me what 2+2 is when you're done."}, "steer"); err != nil {
		t.Fatal(err)
	}
	if c, _ := chatInfo(h, "i"); len(c.Queue) != 0 {
		t.Error("a steer went to the queue")
	}
	if err := h.interrupt("i"); err != nil {
		t.Fatal(err)
	}
	waitChat(t, h, "i", "the interrupted turn's end", ready)
	h.send("i", prompt{ID: "p3", Text: "Say just 'hi'."}, "queue")
	waitChat(t, h, "i", "the next turn's end", func(c wire.ChatInfo) bool { return c.State == wire.AgentReady && c.Last == "hi" })
	items, _ := chatItems(h, "i")
	var texts []string
	for _, it := range items {
		if it.Kind == wire.ChatStatus || it.Kind == wire.ChatUser {
			texts = append(texts, it.Text)
		}
	}
	got := strings.Join(texts, "|")
	if !strings.Contains(got, "sleep|Actually, also tell me what 2+2 is when you're done.|interrupted|Say just 'hi'.") {
		t.Errorf("conversation = %s", got)
	}
	var interrupts int
	for _, m := range heardFrom(t, heard) {
		if req, _ := m["request"].(map[string]any); req != nil && req["subtype"] == "interrupt" {
			interrupts++
		}
	}
	if interrupts != 1 {
		t.Errorf("interrupts sent = %d", interrupts)
	}
}

func TestCodexChatApprovals(t *testing.T) {
	h, heard := testHost(t, "codex", "codex-tools.jsonl")
	h.create(createArgs{ID: "x", Agent: "codex", Mode: "default", Prompt: &prompt{ID: "p1", Text: "create hello.txt"}})
	for i, tool := range []string{"Shell", "Shell", "Edit"} {
		c := waitChat(t, h, "x", "approval "+tool, func(c wire.ChatInfo) bool { return c.Pending != nil && c.Pending.Tool == tool })
		if c.Pending.Call == nil {
			t.Fatalf("approval %d has no call: %+v", i, c.Pending)
		}
		if tool == "Edit" && (c.Pending.Call.Edit == nil || len(c.Pending.Call.Edit.Hunks) == 0) {
			t.Errorf("the edit approval shows the change: %+v", c.Pending.Call)
		}
		h.decide("x", c.Pending.ID, decision{Behavior: "allow"})
	}
	c := waitChat(t, h, "x", "the end of the turn", ready)
	if c.Last != "done" {
		t.Errorf("last = %q", c.Last)
	}
	h.mu.Lock()
	thread := h.chats["x"].rec.Conversation
	h.mu.Unlock()
	if thread != "01a11deb-86b0-7792-8e98-21164225d557" {
		t.Errorf("thread = %q", thread)
	}
	var accepts int
	var start map[string]any
	for _, m := range heardFrom(t, heard) {
		if r, _ := m["result"].(map[string]any); r != nil && r["decision"] == "accept" {
			accepts++
		}
		if m["method"] == "turn/start" {
			start, _ = m["params"].(map[string]any)
		}
	}
	if accepts != 3 {
		t.Errorf("accepts = %d", accepts)
	}
	if start["approvalPolicy"] != "on-request" || start["sandboxPolicy"].(map[string]any)["type"] != "workspaceWrite" {
		t.Errorf("turn/start = %+v", start)
	}
}

func TestCodexChatQuestionAndPlan(t *testing.T) {
	h, heard := testHost(t, "codex", "codex-question.jsonl")
	h.create(createArgs{ID: "x", Agent: "codex", Mode: "plan", Model: "gpt-6-luna", Prompt: &prompt{ID: "p1", Text: "plan a function"}})
	c := waitChat(t, h, "x", "the question", pendingKind("question"))
	q := c.Pending.Questions[0]
	if q.ID != "operation" || !q.Other {
		t.Fatalf("question = %+v", q)
	}
	h.decide("x", c.Pending.ID, decision{Behavior: "allow", Answers: map[string]string{"operation": "mul"}})
	c = waitChat(t, h, "x", "the plan", pendingKind("plan"))
	if !strings.Contains(c.Pending.Plan, "mul(a, b)") || c.State != wire.AgentWaiting {
		t.Fatalf("plan = %+v", c)
	}
	h.decide("x", c.Pending.ID, decision{Behavior: "allow"})
	c = waitChat(t, h, "x", "the implementation's end", func(c wire.ChatInfo) bool {
		return c.State == wire.AgentReady && strings.Contains(c.Last, "mul")
	})
	if c.Mode != "default" {
		t.Errorf("mode after approving the plan = %q", c.Mode)
	}
	var modes []string
	var answered bool
	for _, m := range heardFrom(t, heard) {
		if m["method"] == "turn/start" {
			p := m["params"].(map[string]any)
			modes = append(modes, p["collaborationMode"].(map[string]any)["mode"].(string))
		}
		if r, _ := m["result"].(map[string]any); r != nil {
			if a, _ := r["answers"].(map[string]any); a["operation"] != nil {
				answered = true
			}
		}
	}
	if strings.Join(modes, ",") != "plan,default" || !answered {
		t.Errorf("turns in modes %v, answered %v", modes, answered)
	}
}

func TestCodexChatSteerAndInterrupt(t *testing.T) {
	h, heard := testHost(t, "codex", "codex-interrupt.jsonl")
	h.create(createArgs{ID: "x", Agent: "codex", Prompt: &prompt{ID: "p1", Text: "sleep"}})
	waitChat(t, h, "x", "a running command", func(wire.ChatInfo) bool {
		items, _ := chatItems(h, "x")
		for _, it := range items {
			if it.Tool != nil && it.Tool.Status == wire.ToolRunning {
				return true
			}
		}
		return false
	})
	if err := h.send("x", prompt{ID: "p2", Text: "Also tell me what 2+2 is."}, "steer"); err != nil {
		t.Fatal(err)
	}
	if err := h.interrupt("x"); err != nil {
		t.Fatal(err)
	}
	waitChat(t, h, "x", "the interrupted turn's end", ready)
	h.send("x", prompt{ID: "p3", Text: "Say just 'hi'."}, "queue")
	waitChat(t, h, "x", "the next turn", func(c wire.ChatInfo) bool { return c.State == wire.AgentReady && c.Last == "hi" })
	var methods []string
	for _, m := range heardFrom(t, heard) {
		if s, _ := m["method"].(string); strings.HasPrefix(s, "turn/") {
			methods = append(methods, s)
		}
	}
	if strings.Join(methods, ",") != "turn/start,turn/steer,turn/interrupt,turn/start" {
		t.Errorf("turn calls = %v", methods)
	}
}

// A chat found mid-turn at start was cut off by whatever stopped the
// supervisor: it says so, and is free.
func TestChatAfterARestart(t *testing.T) {
	home := t.TempDir()
	if err := writeChat(home, createArgs{ID: "c", Agent: "claude", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	h := newChatHost(home)
	h.load()
	h.mu.Lock()
	h.setState(h.chats["c"], wire.AgentWorking, "")
	h.mu.Unlock()

	h2 := newChatHost(home)
	h2.load()
	c, _ := chatInfo(h2, "c")
	items, _ := chatItems(h2, "c")
	if c.State != wire.AgentReady || len(items) != 1 || !strings.Contains(items[0].Text, "interrupted") {
		t.Errorf("after a restart: %+v %+v", c, items)
	}
}

// A program that keeps failing to start puts its chat in the error state
// instead of retrying forever.
func TestChatThatKeepsFailing(t *testing.T) {
	home := t.TempDir()
	h := newChatHost(home)
	h.program = func(string) (string, string) { return "", "no claude here" }
	h.create(createArgs{ID: "c", Agent: "claude", Prompt: &prompt{ID: "p1", Text: "hi"}})
	c := waitChat(t, h, "c", "the error state", func(c wire.ChatInfo) bool { return c.State == wire.AgentError })
	if !strings.Contains(c.Error, "no claude here") || len(c.Queue) != 1 {
		t.Errorf("chat = %+v", c)
	}
}

func TestChatCompaction(t *testing.T) {
	dir := t.TempDir()
	l, err := openChatLog(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 64<<10)
	for range 80 {
		l.put(wire.ChatItem{ID: "same", Kind: wire.ChatTool, Text: big})
	}
	l.put(wire.ChatItem{ID: "other", Kind: wire.ChatUser, Text: "hi"})
	if err := l.compact(); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(l.path()) != "log-2.jsonl" {
		t.Fatalf("log = %s", l.path())
	}
	items, size, _ := readItems(l.path())
	if len(items) != 2 || size > 200<<10 {
		t.Errorf("compacted to %d items, %d bytes", len(items), size)
	}
	if _, err := os.Stat(filepath.Join(dir, "log-1.jsonl")); err == nil {
		t.Error("the old generation stayed")
	}
}

func TestChecks(t *testing.T) {
	h := newChatHost(t.TempDir())
	if err := h.create(createArgs{ID: "../x", Agent: "claude"}); err == nil {
		t.Error("a path as a chat id")
	}
	if err := h.create(createArgs{ID: "x", Agent: "gemini"}); err == nil {
		t.Error("an unknown agent")
	}
	if err := h.create(createArgs{ID: "x", Agent: "codex", Mode: "acceptEdits"}); err == nil {
		t.Error("a mode codex doesn't have")
	}
	if err := h.create(createArgs{ID: "x", Agent: "claude", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := h.configure(configureArgs{Chat: "x", Mode: "readOnly"}); err == nil {
		t.Error("a mode claude doesn't have")
	}
	if err := h.configure(configureArgs{Chat: "x", Model: "opus", Effort: "high", Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	c, _ := chatInfo(h, "x")
	if c.Model != "opus" || c.Effort != "high" || c.Title != "Renamed" {
		t.Errorf("configured = %+v", c)
	}
	if err := h.send("x", prompt{ID: "a b", Text: "hi"}, "queue"); err == nil {
		t.Error("a bad prompt id")
	}
	if err := h.closeChat("x"); err != nil {
		t.Fatal(err)
	}
	if _, found := chatInfo(h, "x"); found {
		t.Error("a closed chat is still listed")
	}
	if _, err := os.Stat(filepath.Join(h.home, ".pier", "chats-closed", "x", "chat.json")); err != nil {
		t.Error("a closed chat is kept aside")
	}
}

func TestClaudeArgs(t *testing.T) {
	got := strings.Join(claudeArgs(chatRecord{Conversation: "c-1", Model: "opus", Mode: "plan", Effort: "high"}), " ")
	for _, want := range []string{"--input-format stream-json", "--permission-prompt-tool stdio", "--session-id c-1", "--model opus", "--permission-mode plan", "--effort high"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	if got := strings.Join(claudeArgs(chatRecord{Conversation: "c-1", Started: true}), " "); !strings.Contains(got, "--resume c-1") || strings.Contains(got, "--session-id") {
		t.Errorf("a started conversation resumes: %q", got)
	}
}

func TestClaudeDecision(t *testing.T) {
	in := json.RawMessage(`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`)
	resp, _, err := claudeDecision(in, nil, "question", decision{Behavior: "allow", Answers: map[string]string{"Which database?": "Postgres"}, Notes: map[string]string{"Which database?": "we run it already"}})
	if err != nil {
		t.Fatal(err)
	}
	got := resp["updatedInput"].(map[string]any)
	if got["answers"].(map[string]string)["Which database?"] != "Postgres" || got["questions"] == nil ||
		got["annotations"].(map[string]any)["Which database?"].(map[string]string)["notes"] != "we run it already" {
		t.Errorf("updated input = %+v", got)
	}
	resp, mode, _ := claudeDecision(json.RawMessage(`{"plan":"1. do it"}`), nil, "plan", decision{Behavior: "allow", Mode: "acceptEdits"})
	if mode != "acceptEdits" || resp["updatedPermissions"] == nil {
		t.Errorf("plan approval = %+v %q", resp, mode)
	}
	if _, mode, _ := claudeDecision(json.RawMessage(`{}`), nil, "plan", decision{Behavior: "allow"}); mode != "default" {
		t.Errorf("an approved plan leaves plan mode: %q", mode)
	}
	resp, _, _ = claudeDecision(nil, nil, "plan", decision{Behavior: "deny", Message: "split step 2"})
	if resp["behavior"] != "deny" || resp["message"] != "split step 2" {
		t.Errorf("keep planning = %+v", resp)
	}
	sug := json.RawMessage(`[{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"allow","destination":"session"}]`)
	resp, _, _ = claudeDecision(json.RawMessage(`{"command":"ls"}`), sug, "tool", decision{Behavior: "allowSession"})
	if string(resp["updatedPermissions"].(json.RawMessage)) != string(sug) {
		t.Errorf("allow for the session = %+v", resp)
	}
	if _, _, err := claudeDecision(nil, nil, "plan", decision{Behavior: "allow", Mode: "yolo"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestWithPlan(t *testing.T) {
	home := t.TempDir()
	plan := filepath.Join(home, ".claude", "plans", "p.md")
	os.MkdirAll(filepath.Dir(plan), 0o700)
	os.WriteFile(plan, []byte("# Plan\n1. fix it"), 0o600)
	out := withPlan(home, "ExitPlanMode", json.RawMessage(`{"planFilePath":"`+plan+`"}`))
	if !strings.Contains(string(out), `"plan":"# Plan\n1. fix it"`) {
		t.Errorf("plan = %s", out)
	}
	if out := withPlan(home, "ExitPlanMode", json.RawMessage(`{"planFilePath":"/etc/passwd"}`)); strings.Contains(string(out), `"plan"`) {
		t.Error("a file outside home was read")
	}
}

func TestUserMessage(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	os.WriteFile(png, []byte("\x89PNG fake"), 0o600)
	m := userMessage(prompt{Text: "look", Attachments: []wire.Attachment{{Name: "shot.png", Path: png, Mime: "image/png"}, {Name: "spec.pdf", Path: "/home/agent/.pier/uploads/spec.pdf"}}})
	blocks := m["message"].(map[string]any)["content"].([]map[string]any)
	if len(blocks) != 2 || blocks[0]["type"] != "text" || !strings.Contains(blocks[0]["text"].(string), "spec.pdf") || blocks[1]["type"] != "image" {
		t.Errorf("blocks = %+v", blocks)
	}
	in := codexInput(prompt{Text: "look", Attachments: []wire.Attachment{{Name: "shot.png", Path: png, Mime: "image/png"}}})
	if len(in) != 2 || in[1]["type"] != "localImage" {
		t.Errorf("codex input = %+v", in)
	}
}

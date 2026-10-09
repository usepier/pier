package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/pkg/pier/wire"
)

// A chat continued in a terminal waits, and what happened there comes back
// into it when the terminal program ends.
func TestTakeover(t *testing.T) {
	h, _ := testHost(t, "claude", "claude-tools.jsonl")
	h.create(createArgs{ID: "c1", Agent: "claude", Model: "opus", Mode: "acceptEdits", Prompt: &prompt{ID: "p1", Text: "go"}})
	for range 2 {
		c := waitChat(t, h, "c1", "an approval", pendingKind("tool"))
		if _, err := h.beginTakeover("c1", os.Getpid()); err == nil {
			t.Fatal("a chat mid-turn was taken over")
		}
		h.decide("c1", c.Pending.ID, decision{Behavior: "allow"})
	}
	waitChat(t, h, "c1", "the end of the turn", ready)

	tc, err := h.beginTakeover("c1", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(tc.Argv, " ")
	h.mu.Lock()
	conv := h.chats["c1"].rec.Conversation
	h.mu.Unlock()
	if !strings.Contains(argv, "--resume "+conv) || !strings.Contains(argv, "--model opus") || !strings.Contains(argv, "--permission-mode acceptEdits") {
		t.Errorf("argv = %s", argv)
	}
	if c, _ := chatInfo(h, "c1"); c.State != wire.AgentTerminal {
		t.Fatalf("state = %s", c.State)
	}
	if _, err := h.beginTakeover("c1", os.Getpid()); err == nil {
		t.Error("a chat taken over twice")
	}
	// A prompt sent meanwhile waits for the terminal to end.
	h.send("c1", prompt{ID: "p2", Text: "after"}, "queue")
	if c, _ := chatInfo(h, "c1"); len(c.Queue) != 1 {
		t.Errorf("queue = %+v", c.Queue)
	}

	// What the terminal added to claude's record of the conversation.
	transcript := filepath.Join(h.home, ".claude", "projects", "-home-agent-work-repo", conv+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"type":"user","uuid":"tu1","timestamp":"2026-10-09T10:00:00Z","message":{"role":"user","content":"typed in the terminal"}}
{"type":"assistant","uuid":"ta1","timestamp":"2026-10-09T10:00:01Z","message":{"id":"msg_t","role":"assistant","content":[{"type":"text","text":"answered there"}]}}
`), 0o600)
	if err := h.endTakeover("c1"); err != nil {
		t.Fatal(err)
	}
	items, _ := chatItems(h, "c1")
	var texts []string
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	got := strings.Join(texts, "|")
	if !strings.Contains(got, "continued in a terminal|typed in the terminal|answered there|back from the terminal") {
		t.Errorf("items = %s", got)
	}
	c := waitChat(t, h, "c1", "the queued prompt delivered", func(c wire.ChatInfo) bool { return len(c.Queue) == 0 })
	if c.State == wire.AgentTerminal {
		t.Errorf("state after the terminal = %s", c.State)
	}
}

// A terminal program that went away without a word ends its take-over.
func TestTakeoverReaped(t *testing.T) {
	home := t.TempDir()
	h := newChatHost(home)
	h.program = func(string) (string, string) { return "/bin/claude", "" }
	h.create(createArgs{ID: "c", Agent: "claude"})
	if _, err := h.beginTakeover("c", 1<<22+7); err != nil {
		t.Fatal(err)
	}
	h.reapTakeovers()
	if c, _ := chatInfo(h, "c"); c.State != wire.AgentReady {
		t.Errorf("state = %s", c.State)
	}
}

func TestTakeoverArgvCodex(t *testing.T) {
	got := strings.Join(takeoverArgv("/x/codex", chatRecord{Agent: "codex", Conversation: "th-1", Model: "m", Effort: "high", Mode: "bypassPermissions"}), " ")
	if got != "/x/codex resume th-1 -m m -c model_reasoning_effort=high --dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("argv = %s", got)
	}
}

// The terminal view prints a chat as a log: streamed text once, tools with
// their outcome, what it waits on.
func TestViewPrints(t *testing.T) {
	var b bytes.Buffer
	v := &viewer{w: &b, printed: map[string]wire.ChatItem{}, streamed: map[string]bool{}, cur: "c",
		chats: []wire.ChatInfo{{ChatSummary: wire.ChatSummary{ID: "c", Title: "Fix", State: wire.AgentWorking}}}}
	v.item(wire.ChatItem{ID: "u", Kind: wire.ChatUser, Text: "fix it"})
	v.delta(wire.ChatDelta{ID: "a", Kind: wire.ChatAssistant, Text: "On "})
	v.delta(wire.ChatDelta{ID: "a", Kind: wire.ChatAssistant, Text: "it."})
	v.item(wire.ChatItem{ID: "a", Kind: wire.ChatAssistant, Text: "On it."})
	v.item(wire.ChatItem{ID: "t", Kind: wire.ChatTool, Tool: &wire.ToolCall{Name: "Bash", Summary: "make test", Status: wire.ToolRunning, Bash: &wire.BashView{}}})
	v.item(wire.ChatItem{ID: "t", Kind: wire.ChatTool, Tool: &wire.ToolCall{Name: "Bash", Summary: "make test", Status: wire.ToolOK, Bash: &wire.BashView{Output: "PASS"}}})
	v.state = "caught-up"
	v.chats[0].State = wire.AgentWaiting
	v.chats[0].Pending = &wire.PermissionRequest{ID: "p", Kind: "tool", Tool: "Bash", Summary: "rm -rf build"}
	v.status()
	out := b.String()
	for _, want := range []string{"› fix it", "On it.", "Bash", "make test", "✓", "PASS", "waiting for your approval: Bash rm -rf build"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "On it.") != 0 && strings.Count(out, "On it.") != 1 || strings.Count(out, "it.") > 1 {
		t.Errorf("streamed text printed twice:\n%s", out)
	}
}

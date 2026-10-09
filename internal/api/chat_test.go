package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func item(id string, kind wire.ChatKind, text string) string {
	b, _ := json.Marshal(wire.ChatItem{ID: id, Kind: kind, Text: text, Time: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)})
	return string(b)
}

func toolItem(id string, status wire.ToolStatus) string {
	b, _ := json.Marshal(wire.ChatItem{ID: id, Kind: wire.ChatTool, Tool: &wire.ToolCall{ID: id, Name: "Bash", View: wire.ToolBash, Status: status, Summary: "make test"}})
	return string(b)
}

func logChunk(chat string, off int64, reset bool, lines ...string) pier.LogChunk {
	data := strings.Join(lines, "\n") + "\n"
	return pier.LogChunk{Chat: chat, File: "log-1.jsonl", Off: off, Next: off + int64(len(data)), Data: data, Reset: reset, EOF: true}
}

const chatsWaiting = `{"chats":[{"id":"c1","agent":"claude","title":"Fix login","state":"waiting","waiting":"permission","since":"2026-10-08T10:00:01Z","queued":1,
	"pending":{"id":"p1","kind":"tool","tool":"Bash","summary":"make test","at":"2026-10-08T10:00:01Z"},
	"queue":[{"id":"q1","text":"then push","at":"2026-10-08T10:00:01Z"}],"created":"2026-10-08T10:00:00Z"}]}`

func readChange(t *testing.T, c *client) wire.ChatChange {
	t.Helper()
	var ch wire.ChatChange
	remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &ch)
	return ch
}

func TestChatWatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	first := logChunk("c1", 0, true, item("u1", wire.ChatUser, "fix the login"), toolItem("t1", wire.ToolRunning))
	f := &fake{
		sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}},
		logs:     []pier.LogChunk{first},
		more:     make(chan [2]string, 4),
		chats:    chatsWaiting,
	}
	c := start(t, f)
	c.call("initialize", map[string]any{"protocol": wire.ProtocolVersion})
	resp, _ := c.call("chats.watch", map[string]any{"id": "i-1"})
	var list wire.ChatsSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &list)
	if !list.Live || len(list.Chats) != 1 || list.Chats[0].Pending == nil || len(list.Chats[0].Queue) != 1 {
		t.Fatalf("chats = %+v", list)
	}
	// The new chat's log is tailed once the follow starts over with it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := f.follows
		f.mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, _ = c.call("chat.watch", map[string]any{"sessionId": "i-1", "chatId": "c1"})
	var snap wire.ChatSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &snap)
	if !snap.Live || len(snap.Items) != 2 || snap.Items[1].Tool == nil || snap.Items[1].Tool.Status != wire.ToolRunning || snap.Chat == nil || snap.Chat.Title != "Fix login" {
		t.Fatalf("snapshot = %+v", snap)
	}

	// Text streams in, then the tool finishes and the reply lands.
	f.more <- [2]string{"delta", `{"chat":"c1","deltas":[{"id":"m2:0","kind":"assistant","text":"Fix"}]}`}
	if ch := readChange(t, c); ch.Op != wire.ChatDeltas || ch.Deltas[0].Text != "Fix" {
		t.Errorf("delta = %+v", ch)
	}
	done := logChunk("c1", first.Next, false, toolItem("t1", wire.ToolOK), item("m2:0", wire.ChatAssistant, "Fixed."))
	b, _ := json.Marshal(done)
	f.more <- [2]string{"log", string(b)}
	var got []wire.ChatChange
	for len(got) < 2 {
		ch := readChange(t, c)
		if ch.Op == wire.ChatState {
			continue
		}
		got = append(got, ch)
	}
	if got[0].Op != wire.ChatUpdate || got[0].Items[0].Tool.Status != wire.ToolOK {
		t.Errorf("update = %+v", got[0])
	}
	if got[1].Op != wire.ChatAppend || len(got[1].Items) != 1 || got[1].Items[0].Text != "Fixed." {
		t.Errorf("append = %+v", got[1])
	}
	// A late delta of a finished item is dropped.
	f.more <- [2]string{"delta", `{"chat":"c1","deltas":[{"id":"m2:0","kind":"assistant","text":"ed."}]}`}
	f.more <- [2]string{"delta", `{"chat":"c1","deltas":[{"id":"m3:0","kind":"assistant","text":"Next"}]}`}
	if ch := readChange(t, c); ch.Op != wire.ChatDeltas || ch.Deltas[0].ID != "m3:0" {
		t.Errorf("only the live item's delta goes out: %+v", ch)
	}

	resp, _ = c.call("chat.history", map[string]any{"sessionId": "i-1", "chatId": "c1", "before": snap.Items[1].ID, "limit": 10})
	var page wire.HistoryPage
	remarshal(t, resp["result"], &page)
	if len(page.Items) != 1 || page.Items[0].Text != "fix the login" || page.More {
		t.Errorf("history = %+v", page)
	}

	resp, _ = c.call("chat.decide", map[string]any{"sessionId": "i-1", "chatId": "c1", "requestId": "p1", "behavior": "allowSession"})
	resp2, _ := c.call("chat.send", map[string]any{"sessionId": "i-1", "chatId": "c1", "prompt": map[string]any{"id": "x1", "text": "and push"}, "mode": "steer"})
	if resp["error"] != nil || resp2["error"] != nil || resp2["result"].(map[string]any)["job"] != nil {
		t.Errorf("decide/send = %v / %v", resp, resp2)
	}
	f.mu.Lock()
	if len(f.decided) != 1 || f.decided[0] != "c1:p1:allowSession" || len(f.prompts) != 1 || f.prompts[0] != "i-1:c1:x1:and push:steer" {
		t.Errorf("decided %v, prompts %v", f.decided, f.prompts)
	}
	f.mu.Unlock()
}

// A parked session's chats come from this machine's copies, and a prompt
// to one (with a file attached while parked) goes by a background job that
// wakes it.
func TestChatParked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m, err := pier.OpenMirror("i-2", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(logChunk("c1", 0, true, item("u1", wire.ChatUser, "fix it"), item("a1", wire.ChatAssistant, "Done."))); err != nil {
		t.Fatal(err)
	}
	m.Close()
	f := &fake{sessions: []pier.Session{{ID: "i-2", Name: "old", State: pier.StateParked,
		Chats: []wire.ChatSummary{{ID: "c1", Agent: "claude", Title: "Fix it", State: wire.AgentReady}}}}}
	c := start(t, f)
	resp, _ := c.call("chats.watch", map[string]any{"id": "i-2"})
	var list wire.ChatsSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &list)
	if list.Live || len(list.Chats) != 1 || list.Chats[0].Title != "Fix it" {
		t.Fatalf("parked chats = %+v", list)
	}
	resp, _ = c.call("chat.watch", map[string]any{"sessionId": "i-2", "chatId": "c1"})
	var snap wire.ChatSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &snap)
	if snap.Live || snap.AsOf == nil || len(snap.Items) != 2 {
		t.Fatalf("parked snapshot = %+v", snap)
	}
	resp, _ = c.call("files.upload", map[string]any{"sessionId": "i-2", "name": "spec.pdf", "data": "aGk="})
	var att wire.Attachment
	remarshal(t, resp["result"], &att)
	if !strings.HasPrefix(att.Path, "staged:") || att.Name != "spec.pdf" {
		t.Fatalf("a parked session's file waits here: %+v", att)
	}
	resp, _ = c.call("chat.send", map[string]any{"sessionId": "i-2", "chatId": "c1", "prompt": map[string]any{"id": "x2", "text": "wake up", "attachments": []any{att}}})
	job, _ := resp["result"].(map[string]any)["job"].(map[string]any)
	if job == nil || job["kind"] != string(pier.JobDeliver) {
		t.Errorf("send to parked = %v", resp)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) != 1 || f.prompts[0] != "deliver i-2:c1:wake up:1 files" {
		t.Errorf("delivery = %v", f.prompts)
	}
}

// A question's answers go back by question id; a new chat starts in the
// session.
func TestChatQuestionAndCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}}
	c := start(t, f)
	c.call("chat.decide", map[string]any{"sessionId": "i-1", "chatId": "c1", "requestId": "q1", "behavior": "allow", "answers": map[string]string{"db": "SQLite"}})
	resp, _ := c.call("chat.create", map[string]any{"sessionId": "i-1", "chatId": "c9", "agent": "codex", "mode": "plan", "prompt": map[string]any{"id": "p1", "text": "plan it"}})
	var created wire.ChatCreated
	remarshal(t, resp["result"], &created)
	resp2, _ := c.call("agents.models", map[string]any{"agent": "codex"})
	var models wire.AgentModels
	remarshal(t, resp2["result"], &models)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.decided) != 1 || f.decided[0] != "c1:q1:allow:db=SQLite" {
		t.Errorf("decided = %v", f.decided)
	}
	if created.ChatID != "c9" || created.Job != nil || len(f.prompts) != 1 || f.prompts[0] != "i-1:new codex:plan it" {
		t.Errorf("created %+v, prompts %v", created, f.prompts)
	}
	if len(models.Models) != 1 || len(models.Modes) != 4 || models.Modes[0].ID != "readOnly" {
		t.Errorf("models = %+v", models)
	}
}

func TestNotices(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{out: bufio.NewWriter(&buf)}
	n := newNotices(s)
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	working := pier.Session{ID: "i-1", Name: "fix", State: pier.StateRunning, Setup: "running",
		Chats: []wire.ChatSummary{{ID: "c1", State: wire.AgentWorking, Since: at}, {ID: "c2", State: wire.AgentWorking, Since: at}}}
	n.poll([]pier.Session{working}) // the first poll only records
	done := working
	done.Setup = "failed"
	done.Chats = []wire.ChatSummary{{ID: "c1", State: wire.AgentReady, Since: at.Add(time.Minute), Last: "All green."},
		{ID: "c2", State: wire.AgentWaiting, Since: at.Add(time.Minute), Ask: "rm -rf build"}}
	n.poll([]pier.Session{done})
	n.poll([]pier.Session{done}) // nothing new: nothing sent
	var kinds []string
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var m struct {
			Method string
			Params wire.Notice
		}
		json.Unmarshal(sc.Bytes(), &m)
		if m.Method == "notify" {
			kinds = append(kinds, m.Params.Kind+":"+m.Params.Body)
		}
	}
	if strings.Join(kinds, ",") != "setup_failed:The repo's setup script exited with an error.,finished:All green.,needs_input:rm -rf build" {
		t.Errorf("notices = %q", kinds)
	}
}

func remarshal(t *testing.T, from, to any) {
	t.Helper()
	b, _ := json.Marshal(from)
	if err := json.Unmarshal(b, to); err != nil {
		t.Fatal(err)
	}
}

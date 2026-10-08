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

const (
	txUser       = `{"type":"user","message":{"role":"user","content":"fix the login"},"uuid":"u1","timestamp":"2026-10-08T10:00:00Z"}`
	txTool       = `{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make test"}}]},"uuid":"a1","timestamp":"2026-10-08T10:00:01Z"}`
	txDone       = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"ok"}]},"uuid":"u2","timestamp":"2026-10-08T10:00:02Z"}`
	txReply      = `{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Fixed."}]},"uuid":"a2","timestamp":"2026-10-08T10:00:03Z"}`
	agentWaiting = `{"agents":[{"pane":"%1","kind":"claude","state":"waiting","waiting":"permission","since":"2026-10-08T10:00:01Z","pending":{"id":"p1","tool":"Bash","input":{"command":"make test"},"at":"2026-10-08T10:00:01Z"}}],"queue":[{"id":"q1","text":"then push","at":"2026-10-08T10:00:01Z"}]}`
)

func chunk(off int64, reset bool, lines ...string) pier.TranscriptChunk {
	data := strings.Join(lines, "\n") + "\n"
	return pier.TranscriptChunk{File: "/home/agent/.claude/projects/w/c.jsonl", Kind: "claude", Off: off, Next: off + int64(len(data)), Data: data, Reset: reset, EOF: true}
}

func TestChatWatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	first := chunk(0, true, txUser, txTool)
	f := &fake{
		sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}},
		tx:       []pier.TranscriptChunk{first},
		more:     make(chan pier.TranscriptChunk, 1),
		agent:    agentWaiting,
	}
	c := start(t, f)
	c.call("initialize", map[string]any{"protocol": wire.ProtocolVersion, "approvals": true})
	resp, _ := c.call("chat.watch", map[string]any{"id": "i-1"})
	var snap wire.ChatSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &snap)
	if !snap.Live || len(snap.Items) != 2 || snap.Items[1].Tool == nil || snap.Items[1].Tool.Status != wire.ToolRunning {
		t.Fatalf("snapshot = %+v", snap)
	}
	if a := snap.Agent; a == nil || a.Pending == nil || a.Pending.Summary != "make test" || len(a.Queue) != 1 || a.Queued != 1 {
		t.Fatalf("agent = %+v", snap.Agent)
	}

	// The tool finishes and the agent replies: one update, one append.
	f.more <- chunk(first.Next, false, txDone, txReply)
	var got []wire.ChatChange
	for len(got) < 2 {
		var ch wire.ChatChange
		remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &ch)
		got = append(got, ch)
	}
	if got[0].Op != wire.ChatUpdate || got[0].Items[0].Tool.Status != wire.ToolOK {
		t.Errorf("update = %+v", got[0])
	}
	if got[1].Op != wire.ChatAppend || len(got[1].Items) != 1 || got[1].Items[0].Text != "Fixed." {
		t.Errorf("append = %+v", got[1])
	}

	resp, _ = c.call("chat.history", map[string]any{"sessionId": "i-1", "before": snap.Items[1].ID, "limit": 10})
	var page wire.HistoryPage
	remarshal(t, resp["result"], &page)
	if len(page.Items) != 1 || page.Items[0].Text != "fix the login" || page.More {
		t.Errorf("history = %+v", page)
	}

	resp, _ = c.call("chat.decide", map[string]any{"sessionId": "i-1", "requestId": "p1", "behavior": "allow"})
	resp2, _ := c.call("chat.send", map[string]any{"sessionId": "i-1", "promptId": "x1", "text": "and push"})
	if resp["error"] != nil || resp2["error"] != nil || resp2["result"].(map[string]any)["job"] != nil {
		t.Errorf("decide/send = %v / %v", resp, resp2)
	}
	f.mu.Lock()
	if len(f.decided) != 1 || f.decided[0] != "p1:allow" || len(f.prompts) != 1 || f.prompts[0] != "i-1:x1:and push" {
		t.Errorf("decided %v, prompts %v", f.decided, f.prompts)
	}
	f.mu.Unlock()
}

// A parked session's chat comes from this machine's copy, and a prompt to
// it goes by a background job that wakes it.
func TestChatParked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m, err := pier.OpenMirror("i-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(chunk(0, true, txUser, txReply)); err != nil {
		t.Fatal(err)
	}
	m.Close()
	f := &fake{sessions: []pier.Session{{ID: "i-2", Name: "old", State: pier.StateParked, Agent: &pier.AgentStatus{Kind: "claude", State: "ended"}}}}
	c := start(t, f)
	resp, _ := c.call("chat.watch", map[string]any{"id": "i-2"})
	var snap wire.ChatSnapshot
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &snap)
	if snap.Live || snap.AsOf == nil || len(snap.Items) != 2 || snap.Agent == nil || snap.Agent.State != "ended" {
		t.Fatalf("parked snapshot = %+v", snap)
	}
	resp, _ = c.call("chat.send", map[string]any{"sessionId": "i-2", "promptId": "x2", "text": "wake up"})
	job, _ := resp["result"].(map[string]any)["job"].(map[string]any)
	if job == nil || job["kind"] != string(pier.JobDeliver) {
		t.Errorf("send to parked = %v", resp)
	}
}

func TestNotices(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{out: bufio.NewWriter(&buf)}
	n := newNotices(s)
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	working := pier.Session{ID: "i-1", Name: "fix", State: pier.StateRunning, Setup: "running", Agent: &pier.AgentStatus{State: "working", Since: at}}
	n.poll([]pier.Session{working}) // the first poll only records
	done := working
	done.Setup = "failed"
	done.Agent = &pier.AgentStatus{State: "ready", Since: at.Add(time.Minute), Last: "All green."}
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
	if strings.Join(kinds, ",") != "setup_failed:The repo's setup script exited with an error.,finished:All green." {
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

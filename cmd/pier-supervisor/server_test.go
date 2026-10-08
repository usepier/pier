package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dialHub(t *testing.T) (*hub, net.Conn, *bufio.Scanner) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	old := sockPath
	sockPath = filepath.Join(dir, "s.sock")
	t.Cleanup(func() { sockPath = old })
	h := newHub()
	go h.serve()
	var conn net.Conn
	for range 50 {
		if conn, err = net.Dial("unix", sockPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	sc := bufio.NewScanner(conn)
	return h, conn, sc
}

func next(t *testing.T, conn net.Conn, sc *bufio.Scanner, ev string) message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for sc.Scan() {
		var m message
		json.Unmarshal(sc.Bytes(), &m)
		if m.Ev == ev {
			return m
		}
	}
	t.Fatalf("no %s message", ev)
	return message{}
}

func TestStreamHelloAndTopics(t *testing.T) {
	h, conn, sc := dialHub(t)
	hello := next(t, conn, sc, "hello")
	var hd struct{ Proto int }
	json.Unmarshal(hello.Data, &hd)
	if hd.Proto != streamProto {
		t.Errorf("hello = %s", hello.Data)
	}
	h.publish("status", status{State: "idle"})
	conn.Write([]byte(`{"op":"subscribe","topics":["status"]}` + "\n"))
	if m := next(t, conn, sc, "status"); string(m.Data) == "" {
		t.Error("a new subscriber gets the latest status at once")
	}
	h.publish("status", status{State: "working"})
	m := next(t, conn, sc, "status")
	var st status
	json.Unmarshal(m.Data, &st)
	if st.State != "working" {
		t.Errorf("update = %s", m.Data)
	}
	if h.watching("env") {
		t.Error("nobody asked for env")
	}
}

// An op runs once per id however often it is asked for, and the retry gets
// the first run's answer.
func TestOpsRunOncePerID(t *testing.T) {
	var runs atomic.Int32
	ops["count"] = func(json.RawMessage) (any, error) {
		runs.Add(1)
		return map[string]int{"n": int(runs.Load())}, nil
	}
	defer delete(ops, "count")
	_, conn, sc := dialHub(t)
	for range 2 {
		conn.Write([]byte(`{"id":"op-1","op":"count"}` + "\n"))
		m := next(t, conn, sc, "result")
		if m.OK == nil || !*m.OK || string(m.Data) != `{"n":1}` {
			t.Errorf("result = %+v %s", m, m.Data)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("ran %d times", runs.Load())
	}
	conn.Write([]byte(`{"id":"op-2","op":"container","args":{"action":"rm -rf","container":"x"}}` + "\n"))
	if m := next(t, conn, sc, "result"); m.OK == nil || *m.OK {
		t.Error("unknown container actions are refused")
	}
}

func readTx(t *testing.T, conn net.Conn, sc *bufio.Scanner) txChunk {
	t.Helper()
	var ch txChunk
	if err := json.Unmarshal(next(t, conn, sc, "tx").Data, &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

// The transcript goes out in complete lines from the client's offset, a
// line still being written waits, and a new conversation starts over.
func TestStreamTailsTheTranscript(t *testing.T) {
	h, conn, sc := dialHub(t)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	dir := t.TempDir()
	first := filepath.Join(dir, "a.jsonl")
	os.WriteFile(first, []byte("{\"n\":1}\n{\"n\":2}\n{\"n\":"), 0o600)
	var path atomic.Value
	path.Store(first)
	h.mu.Lock()
	h.findTranscript = func() (string, string) { return path.Load().(string), "claude" }
	h.tailEvery, h.tailChunk = 20*time.Millisecond, 16
	h.mu.Unlock()

	next(t, conn, sc, "hello")
	conn.Write([]byte(`{"op":"tail","args":{"file":"` + first + `","from":8}}` + "\n"))
	ch := readTx(t, conn, sc)
	if ch.Reset || ch.Off != 8 || ch.Data != "{\"n\":2}\n" || ch.Next != 16 || !ch.EOF {
		t.Fatalf("resume = %+v", ch)
	}
	f, _ := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("3}\n" + `{"long":"` + strings.Repeat("x", 40) + "\"}\n")
	f.Close()
	if ch = readTx(t, conn, sc); ch.Data != "{\"n\":3}\n" || ch.EOF {
		t.Fatalf("completed line = %+v", ch)
	}
	if ch = readTx(t, conn, sc); !strings.HasPrefix(ch.Data, `{"long"`) || !ch.EOF {
		t.Fatalf("a line longer than a chunk goes whole: %+v", ch)
	}

	second := filepath.Join(dir, "b.jsonl")
	os.WriteFile(second, []byte("{\"n\":9}\n"), 0o600)
	path.Store(second)
	if ch = readTx(t, conn, sc); !ch.Reset || ch.File != second || ch.Off != 0 || ch.Data != "{\"n\":9}\n" {
		t.Fatalf("new conversation = %+v", ch)
	}
}

func TestFindTranscript(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "projects", "-home-agent-work-shop", "c-1.jsonl")
	codex := filepath.Join(home, ".codex", "sessions", "2026", "10", "08", "rollout-2026-10-08T10-00-00-t-1.jsonl")
	for _, p := range []string{claude, codex} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("{}\n"), 0o600)
	}
	if got := findTranscript(home, agentState{Kind: "claude", Conversation: "c-1"}); got != claude {
		t.Errorf("claude by conversation = %q", got)
	}
	if got := findTranscript(home, agentState{Kind: "codex", Conversation: "t-1"}); got != codex {
		t.Errorf("codex by thread = %q", got)
	}
	if got := findTranscript(home, agentState{Kind: "codex", Since: time.Now()}); got != codex {
		t.Errorf("codex, newest since it started = %q", got)
	}
	if got := findTranscript(home, agentState{Kind: "claude", Transcript: "/etc/passwd"}); got != "" {
		t.Errorf("a path outside home is never read: %q", got)
	}
}

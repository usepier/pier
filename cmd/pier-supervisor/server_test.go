package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
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

func readLog(t *testing.T, conn net.Conn, sc *bufio.Scanner) logChunk {
	t.Helper()
	var ch logChunk
	if err := json.Unmarshal(next(t, conn, sc, "log").Data, &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

// A chat's log goes out in complete lines from the client's offset, what
// is appended follows at once, and a compacted log starts over.
func TestStreamTailsAChatLog(t *testing.T) {
	h, conn, sc := dialHub(t)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	home := t.TempDir()
	host := newChatHost(home)
	h.host = host
	if err := writeChat(home, createArgs{ID: "c1", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	host.load()
	l := host.logOf("c1")
	l.put(wire.ChatItem{ID: "a", Kind: wire.ChatUser, Text: "one"}, wire.ChatItem{ID: "b", Kind: wire.ChatAssistant, Text: "two"})
	first, _ := os.ReadFile(l.path())
	cut := int64(strings.IndexByte(string(first), '\n') + 1)

	next(t, conn, sc, "hello")
	conn.Write([]byte(`{"op":"tail","args":{"chat":"c1","file":"log-1.jsonl","from":` + strconv.FormatInt(cut, 10) + `}}` + "\n"))
	ch := readLog(t, conn, sc)
	if ch.Chat != "c1" || ch.Reset || ch.Off != cut || !strings.Contains(ch.Data, `"id":"b"`) || !ch.EOF {
		t.Fatalf("resume = %+v", ch)
	}
	streaming := next(t, conn, sc, "streaming")
	if !strings.Contains(string(streaming.Data), `"chat":"c1"`) {
		t.Errorf("streaming = %s", streaming.Data)
	}
	l.put(wire.ChatItem{ID: "c", Kind: wire.ChatAssistant, Text: "three"})
	if ch = readLog(t, conn, sc); !strings.Contains(ch.Data, `"id":"c"`) {
		t.Fatalf("appended = %+v", ch)
	}
	host.deltas = h.deltas
	h.deltas("c1", []wire.ChatDelta{{ID: "d", Kind: wire.ChatAssistant, Text: "fo"}})
	if d := next(t, conn, sc, "delta"); !strings.Contains(string(d.Data), `"text":"fo"`) {
		t.Errorf("delta = %s", d.Data)
	}

	conn.Write([]byte(`{"op":"tail","args":{"chat":"c1","file":"log-0.jsonl","from":5}}` + "\n"))
	if ch = readLog(t, conn, sc); !ch.Reset || ch.File != "log-1.jsonl" || ch.Off != 0 {
		t.Fatalf("a copy of another log starts over: %+v", ch)
	}
}

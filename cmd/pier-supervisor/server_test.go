package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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

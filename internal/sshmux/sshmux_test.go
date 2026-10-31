package sshmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSSH puts an ssh on PATH that plays OpenSSH's multiplexing roles: a
// master holds its control "socket" (a file) while its stdin stays open; -O
// operations succeed while it exists; clients run their script locally only
// through a live master.
func fakeSSH(t *testing.T) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "masters.log")
	script := `#!/bin/sh
ctl=""; master=0; op=""
while [ $# -gt 0 ]; do
  case "$1" in
    -S) ctl="$2"; shift 2 ;;
    -O) op="$2"; shift 2 ;;
    -o) case "$2" in ControlMaster=yes) master=1 ;; ControlPath=*) ctl="${2#ControlPath=}" ;; esac; shift 2 ;;
    -e|-i|-L|-R) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
dest="$1"; shift
if [ -n "$op" ]; then [ -e "$ctl" ] && exit 0; exit 255; fi
if [ $master = 1 ]; then
  if [ -e "` + bin + `/fail" ]; then echo "connect to host: refused" >&2; exit 255; fi
  echo "$dest" >> "` + log + `"
  touch "$ctl"; cat >/dev/null; rm -f "$ctl"; exit 0
fi
[ -e "$ctl" ] || { echo "no master" >&2; exit 255; }
exec sh -c "$*"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return log
}

func masters(t *testing.T, log string) int {
	b, _ := os.ReadFile(log)
	return strings.Count(string(b), "\n")
}

func target(context.Context, string) ([]string, string, error) {
	return []string{"-o", "BatchMode=yes"}, "agent@vm", nil
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestOneMasterServesManyCalls(t *testing.T) {
	log := fakeSSH(t)
	m := New(t.TempDir(), target)
	defer m.Close()
	for i := range 3 {
		c, err := m.Conn(ctx(t), "i-1")
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.Exec(ctx(t), "echo hello", nil)
		if err != nil || strings.TrimSpace(string(out)) != "hello" {
			t.Fatalf("call %d: %q %v", i, out, err)
		}
	}
	if n := masters(t, log); n != 1 {
		t.Errorf("three calls started %d masters, want 1", n)
	}
}

// Closing the manager ends its masters: nothing outlives the process that
// started it.
func TestCloseEndsMasters(t *testing.T) {
	fakeSSH(t)
	dir := t.TempDir()
	m := New(dir, target)
	c, err := m.Conn(ctx(t), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	ctl := c.ctl
	m.Close()
	if _, err := os.Stat(ctl); !os.IsNotExist(err) {
		t.Error("the control socket must go with the master")
	}
	if _, err := m.Conn(ctx(t), "i-1"); err != ErrClosed {
		t.Errorf("a closed manager refuses new masters, got %v", err)
	}
}

// A failed connect backs off instead of hammering a down session, and the
// next real attempt first forgets how the session was reached.
func TestFailureBacksOffAndInvalidates(t *testing.T) {
	log := fakeSSH(t)
	bin := filepath.Dir(log)
	os.WriteFile(filepath.Join(bin, "fail"), nil, 0o644)
	var invalidated atomic.Int32
	m := New(t.TempDir(), target)
	m.Invalidate = func(string) { invalidated.Add(1) }
	defer m.Close()
	if _, err := m.Conn(ctx(t), "i-1"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("first attempt = %v", err)
	}
	if _, err := m.Conn(ctx(t), "i-1"); err == nil || !strings.Contains(err.Error(), "retrying in") {
		t.Fatalf("an immediate retry must back off, got %v", err)
	}
	os.Remove(filepath.Join(bin, "fail"))
	c := m.conns["i-1"]
	c.mu.Lock()
	c.failedAt = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	if _, err := m.Conn(ctx(t), "i-1"); err != nil {
		t.Fatalf("after the backoff a reachable session connects: %v", err)
	}
	if invalidated.Load() != 1 {
		t.Errorf("Invalidate ran %d times, want 1", invalidated.Load())
	}
}

// A master that dies is replaced on the next call.
func TestDeadMasterIsReplaced(t *testing.T) {
	log := fakeSSH(t)
	m := New(t.TempDir(), target)
	defer m.Close()
	c, err := m.Conn(ctx(t), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.master.Process.Kill()
	exited := c.exited
	c.mu.Unlock()
	<-exited
	c2, err := m.Conn(ctx(t), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c2.Exec(ctx(t), "echo back", nil); err != nil || strings.TrimSpace(string(out)) != "back" {
		t.Fatalf("exec after replacement: %q %v", out, err)
	}
	if n := masters(t, log); n != 2 {
		t.Errorf("masters started = %d, want 2", n)
	}
}

func TestCleanStaleKeepsLiveOwners(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, target)
	mine := filepath.Join(dir, "deadbeef-ignored")
	os.WriteFile(m.socketPath("i-1"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "7ffffffe-abcdef012345"), nil, 0o600) // no such pid
	os.WriteFile(mine, nil, 0o600)
	m.CleanStale()
	if _, err := os.Stat(m.socketPath("i-1")); err != nil {
		t.Error("this process's sockets stay")
	}
	if _, err := os.Stat(filepath.Join(dir, "7ffffffe-abcdef012345")); !os.IsNotExist(err) {
		t.Error("a dead process's socket goes")
	}
	if len(filepath.Base(m.socketPath("an-instance-name-that-is-long-enough-to-matter-on-gcp-1234567890"))) > 24 {
		t.Error("socket names stay short whatever the session id")
	}
}

package proxy

// One proxy per machine, however it was started. Whoever runs it — `pier
// proxy` in a terminal, or the background process the app and `pier proxy
// start` launch — answers on a control socket in pier's state dir: the
// others follow its state there and ask it to stop, instead of starting a
// second one that would fight it for the resolver and the addresses. The
// background one outlives whatever started it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/driver"
)

// ErrRunning: a proxy already runs on this machine.
var ErrRunning = errors.New("the proxy already runs")

// ErrNotRunning: no proxy runs on this machine.
var ErrNotRunning = errors.New("the proxy isn't running")

// ctlPath is the proxy's control socket.
func ctlPath(stateDir string) string { return filepath.Join(stateDir, "proxy", "proxy.sock") }

// logPath is where a background proxy writes what a terminal one prints.
func logPath(stateDir string) string { return filepath.Join(stateDir, "proxy", "proxy.log") }

type ctlMsg struct {
	Op     string  `json:"op,omitempty"` // follow | stop
	Status *Status `json:"status,omitempty"`
	OK     bool    `json:"ok,omitempty"`
}

// Serve runs the proxy (Run) and answers on its control socket until ctx
// ends or someone asks it to stop. ErrRunning when a proxy already does.
func Serve(ctx context.Context, drv driver.Driver, opt Options) error {
	return serveCtl(ctx, opt.StateDir, func(ctx context.Context, onChange func(Status)) error {
		next := opt.OnChange
		opt.OnChange = func(st Status) {
			onChange(st)
			if next != nil {
				next(st)
			}
		}
		return Run(ctx, drv, opt)
	})
}

// serveCtl answers on the control socket while run runs.
func serveCtl(ctx context.Context, stateDir string, run func(ctx context.Context, onChange func(Status)) error) error {
	path := ctlPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return ErrRunning
	}
	_ = os.Remove(path) // a crashed proxy's
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	defer ln.Close()

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	b := &broadcast{subs: map[chan Status]bool{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(conn, stop)
		}
	}()
	defer b.close() // followers hear it end
	return run(ctx, b.set)
}

// broadcast hands the proxy's state to whoever follows it.
type broadcast struct {
	mu     sync.Mutex
	last   *Status
	subs   map[chan Status]bool
	closed bool
}

func (b *broadcast) set(st Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = &st
	for ch := range b.subs {
		select {
		case ch <- st:
		default: // a slow follower gets the next one
		}
	}
}

func (b *broadcast) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
	b.closed = true
}

func (b *broadcast) serve(conn net.Conn, stop context.CancelFunc) {
	defer conn.Close()
	var m ctlMsg
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&m); err != nil {
		return
	}
	enc := json.NewEncoder(conn)
	switch m.Op {
	case "stop":
		stop()
		enc.Encode(ctlMsg{OK: true})
	case "follow":
		ch := make(chan Status, 4)
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		b.subs[ch] = true
		last := b.last
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
		}()
		if last != nil && enc.Encode(ctlMsg{Status: last}) != nil {
			return
		}
		// A follower that hangs up is noticed on the next write.
		for st := range ch {
			st := st
			if enc.Encode(ctlMsg{Status: &st}) != nil {
				return
			}
		}
	}
}

func dial(stateDir string) (net.Conn, error) {
	c, err := net.DialTimeout("unix", ctlPath(stateDir), 2*time.Second)
	if err != nil {
		return nil, ErrNotRunning
	}
	return c, nil
}

// Running reports whether a proxy runs on this machine.
func Running(stateDir string) bool {
	c, err := dial(stateDir)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Follow sends the running proxy's state to fn as it changes, until ctx
// ends or the proxy stops (nil then). ErrNotRunning when none runs.
func Follow(ctx context.Context, stateDir string, fn func(Status)) error {
	c, err := dial(stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	if err := json.NewEncoder(c).Encode(ctlMsg{Op: "follow"}); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var m ctlMsg
		if err := dec.Decode(&m); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil // it stopped
		}
		if m.Status != nil {
			fn(*m.Status)
		}
	}
}

// Stop asks the running proxy to stop, and waits until it has.
func Stop(stateDir string) error {
	c, err := dial(stateDir)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(ctlMsg{Op: "stop"}); err != nil {
		return err
	}
	var m ctlMsg
	_ = json.NewDecoder(c).Decode(&m)
	for range 100 {
		if !Running(stateDir) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the proxy didn't stop")
}

// StartBackground starts argv (pier's own `proxy serve`) as a process of
// its own that outlives the caller, and returns once it answers. The
// network setup it needs must be in place (EnsureNet): it has no one to
// ask for an administrator's password.
func StartBackground(stateDir string, argv []string) error {
	if Running(stateDir) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath(stateDir)), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath(stateDir), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	for {
		if Running(stateDir) {
			return nil
		}
		select {
		case err := <-exited:
			b, _ := os.ReadFile(logPath(stateDir))
			return fmt.Errorf("the proxy stopped as it started (%v): %s", err, lastLine(string(b)))
		case <-deadline:
			return errors.New("the proxy didn't come up")
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

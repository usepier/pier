// Package sshmux keeps one OpenSSH master connection per session and runs
// everything else — status reads, log tails, terminals, port forwards — as
// channels over it. Over a cloud tunnel each fresh ssh is a fresh tunnel
// session (seconds of setup, a helper process); over the master it is a
// channel on a connection that is already up.
//
// A master belongs to the process that started it: its session reads a pipe
// only this process holds, so however the process ends — even killed — the
// pipe closes, the remote side ends, and the master and its forwards go
// with it. Control sockets carry the owner's pid in their name, so a later
// process can tell its own from the debris of a dead one.
package sshmux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Target resolves how to reach a session: ssh option arguments and the
// destination (user@host).
type Target func(ctx context.Context, id string) (opts []string, dest string, err error)

// Manager owns the masters of one process.
type Manager struct {
	// Dir holds the control sockets. macOS caps a socket path at 104 bytes
	// and ssh adds a temporary suffix while binding, so names stay short.
	Dir    string
	Target Target
	// Invalidate forgets how a session was reached (a cached public IP)
	// before a reconnect, so a session that moved is found again.
	Invalidate func(id string)
	// IdleClose closes masters nothing has used for this long; 0 keeps them.
	IdleClose time.Duration

	mu      sync.Mutex
	conns   map[string]*Conn
	closed  bool
	janitor sync.Once
	stopJ   chan struct{}
}

// ErrClosed: the manager was closed.
var ErrClosed = errors.New("connection manager closed")

// Conn is one session's master connection.
type Conn struct {
	m  *Manager
	id string

	mu       sync.Mutex
	ctl      string
	opts     []string
	dest     string
	master   *exec.Cmd
	hold     *os.File // write end of the master's stdin; closing it ends the master
	exited   chan struct{}
	failedAt time.Time
	failures int
	lastErr  error
	lastUse  time.Time
	starting chan struct{} // non-nil while a start is in flight
}

// New returns a manager whose sockets live in dir.
func New(dir string, target Target) *Manager {
	return &Manager{Dir: dir, Target: target, conns: map[string]*Conn{}, stopJ: make(chan struct{})}
}

func (m *Manager) socketPath(id string) string {
	h := sha256.Sum256([]byte(id))
	return filepath.Join(m.Dir, fmt.Sprintf("%x-%s", os.Getpid(), hex.EncodeToString(h[:6])))
}

// CleanStale removes control sockets left by processes that no longer run.
func (m *Manager) CleanStale() {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		pidHex, _, ok := strings.Cut(e.Name(), "-")
		if !ok {
			continue
		}
		pid, err := strconv.ParseInt(pidHex, 16, 64)
		if err != nil {
			continue
		}
		if pid == int64(os.Getpid()) || alive(int(pid)) {
			continue
		}
		os.Remove(filepath.Join(m.Dir, e.Name()))
	}
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Conn returns the session's ready master, starting it if needed and
// waiting at most until ctx ends. After a failure, starts back off (1s,
// 2s, 4s … 30s) so a session that is down isn't hammered.
func (m *Manager) Conn(ctx context.Context, id string) (*Conn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	c := m.conns[id]
	if c == nil {
		c = &Conn{m: m, id: id}
		m.conns[id] = c
	}
	m.mu.Unlock()
	m.janitor.Do(func() {
		if m.IdleClose > 0 {
			go m.reap()
		}
	})
	return c, c.ensure(ctx)
}

func backoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := time.Second << min(failures-1, 5)
	return min(d, 30*time.Second)
}

func (c *Conn) ensure(ctx context.Context) error {
	for {
		c.mu.Lock()
		c.lastUse = time.Now()
		if c.master != nil && c.ready() {
			c.mu.Unlock()
			return nil
		}
		if wait := c.starting; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if until := time.Until(c.failedAt.Add(backoff(c.failures))); until > 0 && c.lastErr != nil {
			err := c.lastErr
			c.mu.Unlock()
			return fmt.Errorf("%w (retrying in %s)", err, until.Round(time.Second))
		}
		done := make(chan struct{})
		c.starting = done
		c.mu.Unlock()

		err := c.start(ctx)

		c.mu.Lock()
		c.starting = nil
		if err != nil {
			c.failures++
			c.failedAt, c.lastErr = time.Now(), err
		} else {
			c.failures, c.lastErr = 0, nil
		}
		c.mu.Unlock()
		close(done)
		return err
	}
}

// ready reports whether the master process still runs. Called with mu held.
func (c *Conn) ready() bool {
	select {
	case <-c.exited:
		c.master = nil
		if c.hold != nil {
			c.hold.Close()
			c.hold = nil
		}
		return false
	default:
		return true
	}
}

func (c *Conn) start(ctx context.Context) error {
	if c.failures > 0 && c.m.Invalidate != nil {
		c.m.Invalidate(c.id)
	}
	opts, dest, err := c.m.Target(ctx, c.id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.m.Dir, 0o700); err != nil {
		return err
	}
	ctl := c.m.socketPath(c.id)
	os.Remove(ctl) // a socket from a master of ours that died
	life, hold, err := os.Pipe()
	if err != nil {
		return err
	}
	args := slices.Concat(opts, []string{
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-o", "ControlPath=" + ctl,
		dest, "cat >/dev/null",
	})
	master := exec.Command("ssh", args...)
	master.Stdin = life
	var stderr bytes.Buffer
	master.Stderr = &stderr
	// A killed master can leave its tunnel helper holding stderr open;
	// don't let that keep Wait (and the exit signal) hanging.
	master.WaitDelay = 2 * time.Second
	if err := master.Start(); err != nil {
		life.Close()
		hold.Close()
		return err
	}
	life.Close()
	exited := make(chan struct{})
	go func() {
		master.Wait()
		close(exited)
	}()
	// Ready once the control socket answers; failed if ssh exits first.
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-exited:
			hold.Close()
			os.Remove(ctl)
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "connection closed"
			}
			return fmt.Errorf("ssh %s: %s", c.id, msg)
		case <-ctx.Done():
			hold.Close()
			master.Process.Kill()
			<-exited
			os.Remove(ctl)
			return ctx.Err()
		case <-tick.C:
			if _, err := os.Stat(ctl); err != nil {
				continue
			}
			if exec.Command("ssh", "-S", ctl, "-O", "check", dest).Run() != nil {
				continue
			}
			c.mu.Lock()
			c.ctl, c.opts, c.dest = ctl, opts, dest
			c.master, c.hold, c.exited = master, hold, exited
			c.mu.Unlock()
			return nil
		}
	}
}

// client returns ssh arguments that ride this master: no new connection,
// never becoming a master itself.
func (c *Conn) client() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastUse = time.Now()
	return []string{"-S", c.ctl, "-o", "ControlMaster=no", c.dest}
}

// Command builds an ssh client process over the master: extra ssh flags
// (-tt for a terminal, -e none) followed by the destination and the remote
// script. The caller wires its stdio and starts it.
func (c *Conn) Command(ctx context.Context, extra []string, script string) *exec.Cmd {
	cl := c.client()
	args := slices.Concat(extra, cl[:4], []string{cl[4], script})
	return exec.CommandContext(ctx, "ssh", args...)
}

// Exec runs a script and returns its stdout. A failure carries stderr.
func (c *Conn) Exec(ctx context.Context, script string, stdin io.Reader) ([]byte, error) {
	cmd := c.Command(ctx, nil, script)
	cmd.Stdin = stdin
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("ssh %s: %s", c.id, msg)
	}
	return out.Bytes(), nil
}

// Forward adds a forward (an ssh -L/-R spec, flag included) to the master.
func (c *Conn) Forward(flag, spec string) error {
	return c.control("forward", flag, spec)
}

// Cancel removes a forward Forward added.
func (c *Conn) Cancel(flag, spec string) error {
	return c.control("cancel", flag, spec)
}

func (c *Conn) control(op, flag, spec string) error {
	cl := c.client()
	out, err := exec.Command("ssh", "-S", cl[1], "-O", op, flag, spec, cl[4]).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh -O %s %s: %s", op, spec, strings.TrimSpace(string(out)))
	}
	return nil
}

// close ends the master. Called without mu held.
func (c *Conn) close() {
	c.mu.Lock()
	hold, master, exited, ctl := c.hold, c.master, c.exited, c.ctl
	c.hold, c.master = nil, nil
	c.mu.Unlock()
	if hold != nil {
		hold.Close()
	}
	if master != nil {
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			master.Process.Kill()
			<-exited
		}
	}
	if ctl != "" {
		os.Remove(ctl)
	}
}

// Existing returns the session's master if one is up, nil otherwise. Callers
// that only poll use it to ride a connection something else keeps open,
// without opening one of their own.
func (m *Manager) Existing(id string) *Conn {
	m.mu.Lock()
	c := m.conns[id]
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.master == nil || !c.ready() {
		return nil
	}
	return c
}

// Drop closes a session's master (it parked, or was removed).
func (m *Manager) Drop(id string) {
	m.mu.Lock()
	c := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// Close ends every master; the manager refuses new ones after.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	conns := m.conns
	m.conns = map[string]*Conn{}
	close(m.stopJ)
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()
}

// reap closes masters idle past IdleClose.
func (m *Manager) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stopJ:
			return
		case <-t.C:
		}
		m.mu.Lock()
		var idle []string
		for id, c := range m.conns {
			c.mu.Lock()
			if c.master != nil && time.Since(c.lastUse) > m.IdleClose && c.starting == nil {
				idle = append(idle, id)
			}
			c.mu.Unlock()
		}
		m.mu.Unlock()
		for _, id := range idle {
			m.Drop(id)
		}
	}
}

// Busy marks a connection used now, so long-lived channels (a terminal, a
// stream) keep the janitor away.
func (c *Conn) Busy() {
	c.mu.Lock()
	c.lastUse = time.Now()
	c.mu.Unlock()
}

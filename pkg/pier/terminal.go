package pier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"

	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/sshmux"
)

// Terminal is one interactive terminal a frontend renders itself: a tab
// into a session's tmux, or a local command (a cloud login). Read returns
// its output, Write sends keystrokes. Closing it ends the ssh client — the
// tmux windows it showed keep running in the session.
type Terminal struct {
	pty  *os.File
	cmd  *exec.Cmd
	done chan struct{}
	code int
	once sync.Once
}

func startTerminal(cmd *exec.Cmd, cols, rows int) (*Terminal, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(cols, 2)), Rows: uint16(max(rows, 1))})
	if err != nil {
		return nil, err
	}
	t := &Terminal{pty: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		t.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.code = ee.ExitCode()
		} else if err != nil {
			t.code = -1
		}
		close(t.done)
	}()
	return t, nil
}

func (t *Terminal) Read(p []byte) (int, error)  { return t.pty.Read(p) }
func (t *Terminal) Write(p []byte) (int, error) { return t.pty.Write(p) }

// Resize tells the terminal (and through ssh, tmux) its new size.
func (t *Terminal) Resize(cols, rows int) error {
	return pty.Setsize(t.pty, &pty.Winsize{Cols: uint16(max(cols, 2)), Rows: uint16(max(rows, 1))})
}

// Done is closed when the terminal's process exits; ExitCode is then set.
func (t *Terminal) Done() <-chan struct{} { return t.done }
func (t *Terminal) ExitCode() int         { return t.code }

// Close hangs the terminal up.
func (t *Terminal) Close() error {
	t.once.Do(func() {
		if t.cmd.Process != nil {
			t.cmd.Process.Signal(syscall.SIGHUP)
		}
		t.pty.Close()
	})
	return nil
}

// OpenTerminal opens a terminal tab into the session's tmux: its own view
// (tab names it) onto main's windows, showing window (a tmux window id, or
// "" for the current one). The terminal attaches a tmux client, so the
// session counts as attached while it is open.
func (c *Client) OpenTerminal(ctx context.Context, s Session, tab, window string, cols, rows int) (*Terminal, error) {
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return nil, err
	}
	c.ensureOpener(s.ID, conn)
	then, err := payload.TabAttach(tab, window)
	if err != nil {
		return nil, err
	}
	// -e none: the tilde escape would let "~." typed after a newline kill
	// the tab. -tt: a terminal even though stdin is a pty we own.
	cmd := conn.Command(context.Background(), []string{"-tt", "-e", "none", "-o", "ForwardAgent=yes"}, payload.AttachScript(then))
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	conn.Busy()
	return startTerminal(cmd, cols, rows)
}

// LocalTerminal runs a command on this machine in a terminal: logins that
// prompt (an MFA code, a device flow) where no terminal of the user's own is
// at hand.
func LocalTerminal(argv []string, cols, rows int) (*Terminal, error) {
	if len(argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	return startTerminal(cmd, cols, rows)
}

// ensureOpener gives a session's connection the laptop's browser: URLs the
// session opens (an agent's login, "open in browser") come back over it.
// Once per connection; a failure leaves terminals working without it.
func (c *Client) ensureOpener(id string, conn *sshmux.Conn) {
	c.openMu.Lock()
	defer c.openMu.Unlock()
	if c.openers == nil {
		c.openers = map[openerKey]func(){}
	}
	key := openerKey{id, conn.Generation()}
	if _, ok := c.openers[key]; ok {
		return
	}
	sock, closeOpener, err := listenOpener()
	if err != nil {
		return
	}
	if err := conn.Forward("-R", remoteOpenSocket+":"+sock); err != nil {
		closeOpener()
		return
	}
	c.openers[key] = closeOpener
}

// openerKey is one master connection of one session: a reconnect needs the
// opener forwarded again.
type openerKey struct {
	id  string
	gen int
}

// Window is one tmux window of a session's main tmux session.
type Window struct {
	ID      string // tmux window id (@N): stable across renames and moves
	Index   int
	Name    string
	Command string // the active pane's foreground command
	Active  bool   // main's current window
	Role    string // what pier made it for: "agent", "setup", or "" for anything else
}

const windowFormat = "#{window_id}|#{window_index}|#{window_active}|#{pane_current_command}|#{@pier-role}|#{window_name}"

// Windows lists the session's tmux windows in order.
func (c *Client) Windows(ctx context.Context, s Session) ([]Window, error) {
	if err := CheckReady(s); err != nil {
		return nil, err
	}
	out, err := c.exec(ctx, s.ID, "tmux list-windows -t main -F '"+windowFormat+"' 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	return parseWindows(out), nil
}

func parseWindows(out string) []Window {
	var ws []Window
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "|", 6)
		if len(f) != 6 || !strings.HasPrefix(f[0], "@") {
			continue
		}
		idx, _ := strconv.Atoi(f[1])
		w := Window{ID: f[0], Index: idx, Active: f[2] == "1", Command: f[3], Role: f[4], Name: f[5]}
		if w.Role == "" && (w.Name == "setup" || w.Name == "setup-failed") {
			w.Role = "setup"
		}
		ws = append(ws, w)
	}
	return ws
}

// NewWindow adds a window to the session's tmux, in the repo's checkout, and
// returns it. name may be empty (tmux names it after its command).
func (c *Client) NewWindow(ctx context.Context, s Session, name string) (Window, error) {
	if err := CheckReady(s); err != nil {
		return Window{}, err
	}
	args := "-d -t main: -P -F '#{window_id}'"
	if name != "" {
		args += " -n " + shellQuote(name)
	}
	script := "tmux has-session -t main 2>/dev/null || tmux new-session -d -s main\n" +
		"cd ~/work/" + shellQuote(s.Repo) + " 2>/dev/null || cd\n" +
		"tmux new-window " + args + " -c \"$PWD\""
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return Window{}, err
	}
	id := strings.TrimSpace(out)
	if !strings.HasPrefix(id, "@") {
		return Window{}, fmt.Errorf("tmux did not report the new window (%q)", id)
	}
	ws, err := c.Windows(ctx, s)
	if err != nil {
		return Window{ID: id, Name: name}, nil
	}
	for _, w := range ws {
		if w.ID == id {
			return w, nil
		}
	}
	return Window{ID: id, Name: name}, nil
}

// KillWindow closes a tmux window and whatever runs in it.
func (c *Client) KillWindow(ctx context.Context, s Session, window string) error {
	if !strings.HasPrefix(window, "@") {
		return fmt.Errorf("bad window id %q", window)
	}
	_, err := c.exec(ctx, s.ID, "tmux kill-window -t "+shellQuote(window))
	return err
}

// RenameWindow renames a tmux window (and stops tmux renaming it after its
// command).
func (c *Client) RenameWindow(ctx context.Context, s Session, window, name string) error {
	if !strings.HasPrefix(window, "@") {
		return fmt.Errorf("bad window id %q", window)
	}
	_, err := c.exec(ctx, s.ID, "tmux rename-window -t "+shellQuote(window)+" "+shellQuote(name))
	return err
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

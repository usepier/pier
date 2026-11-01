package piercore

// terminal.go: a session's tmux, as the Mac's terminal tabs see it — its
// windows, new ones in the checkout, and a live PTY onto one of them.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ensureMain starts tmux's main session when there is none, its first
// window the home shell (internal/driver/payload.EnsureMain).
const ensureMain = "tmux has-session -t main 2>/dev/null || { tmux new-session -d -s main && tmux set-option -w -t main @pier-role home; }"

// windowFormat is pkg/pier's: id, index, active, command, role, name — the
// name last, since it may hold the separator.
const windowFormat = "#{window_id}|#{window_index}|#{window_active}|#{pane_current_command}|#{@pier-role}|#{window_name}"

var windowID = regexp.MustCompile(`^@[0-9]+$`)

func parseWindows(out string) []window {
	ws := []window{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "|", 6)
		if len(f) != 6 || !windowID.MatchString(f[0]) {
			continue
		}
		index, _ := strconv.Atoi(f[1])
		w := window{ID: f[0], Index: index, Active: f[2] == "1", Command: f[3], Role: f[4], Name: f[5]}
		if w.Role == "" && (w.Name == "setup" || w.Name == "setup-failed") {
			w.Role = "setup"
		}
		ws = append(ws, w)
	}
	return ws
}

// newWindow opens a window in the checkout running command (a shell when
// empty), as pkg/pier's NewWindow does.
func (c *Client) newWindow(ctx context.Context, s session, name string, command []string) (window, error) {
	args := "-d -t main: -P -F " + shellQuote(windowFormat)
	if name != "" {
		args += " -n " + shellQuote(name)
	}
	script := ensureMain + "\n" +
		"cd ~/work/" + shellQuote(s.Repo) + " 2>/dev/null || cd\n" +
		"tmux new-window " + args + " -c \"$PWD\""
	if len(command) > 0 {
		script += " -- " + shellJoin(command)
	}
	out, err := c.runRemote(ctx, s.ID, script)
	if err != nil {
		return window{}, err
	}
	ws := parseWindows(out)
	if len(ws) != 1 {
		return window{}, fmt.Errorf("tmux didn't say what it opened (%q)", strings.TrimSpace(out))
	}
	return ws[0], nil
}

// chatWindowScript opens the session's chats in a tmux window of their own,
// as pkg/pier's ChatWindow does: the live read-only view (chatID picked in
// it), or — takeover — a window continuing chatID in its agent's own
// interface. It prints the window's id. A supervisor that predates chats
// would take the command for a start of its loop, so it runs only where
// the beacon lists chats.
func chatWindowScript(chatID string, takeover bool) string {
	script := "grep -q '\"chats\"' /run/pier/status.json 2>/dev/null || { echo \"this session's pier is too old for chats: open it on your Mac once\" >&2; exit 1; }\n" +
		"pier-supervisor chat window"
	if takeover {
		script += " --takeover"
	}
	if chatID != "" {
		script += " " + shellQuote(chatID)
	}
	return script
}

// tabAttach attaches through a grouped session of its own (pier-tab-<tab>),
// as internal/driver/payload.TabAttach does: the supervisor knows those as
// views onto main, not sessions to restore, and they go when detached.
func tabAttach(window string) (string, error) {
	if window != "" && !windowID.MatchString(window) {
		return "", fmt.Errorf("bad window id %q", window)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := "pier-tab-ios" + hex.EncodeToString(b)
	script := ensureMain + "\n" +
		"tmux kill-session -t " + s + " 2>/dev/null\n" +
		"tmux new-session -d -t main -s " + s + "\n"
	if window != "" {
		script += "tmux select-window -t " + s + ":" + window + " 2>/dev/null\n"
	}
	return script + "exec tmux attach-session -t " + s + " \\; set-option -t " + s + " destroy-unattached on", nil
}

// TerminalListener is Swift's: the PTY's output as it comes (on a Go
// goroutine), and its end.
type TerminalListener interface {
	Output(data []byte)
	Closed(message string)
}

// Terminal is a live PTY on one of a session's tmux windows.
type Terminal struct {
	session  *ssh.Session
	stdin    io.WriteCloser
	listener TerminalListener
	mu       sync.Mutex
	closed   bool
}

// OpenTerminal attaches to window (an @id; "" for main's current one) at
// columns × rows.
func (c *Client) OpenTerminal(sessionID, window string, columns, rows int, listener TerminalListener) (*Terminal, error) {
	script, err := tabAttach(window)
	if err != nil {
		return nil, err
	}
	client, err := c.sshClient(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	s, err := client.client.NewSession()
	if err != nil {
		c.dropRemote(sessionID, client)
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	if columns < 1 || rows < 1 {
		columns, rows = 80, 24
	}
	if err := s.RequestPty("xterm-256color", rows, columns, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	if err := s.Start("bash -lc " + shellQuote(script)); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("attach to tmux: %w", err)
	}
	t := &Terminal{session: s, stdin: stdin, listener: listener}
	go t.copyOutput(stdout)
	go t.wait()
	return t, nil
}

func (t *Terminal) copyOutput(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.listener.Output(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) wait() {
	err := t.session.Wait()
	message := ""
	var exit *ssh.ExitError
	if err != nil && !errors.Is(err, io.EOF) && !errors.As(err, &exit) {
		message = err.Error()
	}
	t.mu.Lock()
	was := t.closed
	t.closed = true
	t.mu.Unlock()
	if !was {
		t.listener.Closed(message)
	}
}

// Write sends keystrokes.
func (t *Terminal) Write(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("the terminal has ended")
	}
	_, err := t.stdin.Write(data)
	return err
}

// Resize tells the PTY its new size.
func (t *Terminal) Resize(columns, rows int) error {
	if columns < 1 || rows < 1 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	return t.session.WindowChange(rows, columns)
}

// Close detaches; the window keeps running in the session.
func (t *Terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.stdin.Close()
	return t.session.Close()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func shellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(data), nil
}

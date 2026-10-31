package main

// The chats in a terminal: `pier-supervisor chat view` follows the
// session's chats live, read-only, in a tmux window of its own (role
// "agent", what `pier attach --agent` opens); t continues the chat shown
// in that window's terminal (take-over), n and p switch chats. `chat
// takeover <id>` is the take-over alone, `chat window` makes (or finds) the
// window and prints its id.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/usepier/pier/pkg/pier/wire"
)

func chatMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pier-supervisor chat view [chat] | takeover <chat> | window [--takeover] [chat]")
		return 2
	}
	switch args[0] {
	case "view":
		chatID := ""
		if len(args) > 1 {
			chatID = args[1]
		}
		return viewMain(chatID)
	case "takeover":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: pier-supervisor chat takeover <chat>")
			return 2
		}
		if err := runTakeover(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "pier:", err)
			return 1
		}
		return 0
	case "window":
		return windowMain(args[1:])
	}
	fmt.Fprintln(os.Stderr, "pier-supervisor: unknown chat command", args[0])
	return 2
}

// runTakeover continues a chat in this terminal until its program exits.
func runTakeover(chatID string) error {
	req, _ := json.Marshal(map[string]any{"id": newOpID(), "op": "chat.takeover.begin", "args": map[string]any{"chat": chatID, "pid": os.Getpid()}})
	out, code := opRequest(string(req))
	if code != 0 {
		return errors.New("couldn't continue the chat here")
	}
	var tc takeoverCommand
	if err := json.Unmarshal(out, &tc); err != nil || len(tc.Argv) == 0 {
		return errors.New("the supervisor gave no command")
	}
	cmd := exec.Command(tc.Argv[0], tc.Argv[1:]...)
	cmd.Dir, cmd.Env = tc.Dir, tc.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	end, _ := json.Marshal(map[string]any{"id": newOpID(), "op": "chat.takeover.end", "args": map[string]any{"chat": chatID}})
	opRequest(string(end))
	var exit *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		return runErr
	}
	return nil
}

// windowMain makes sure the chats window exists (or, --takeover, opens one
// continuing a chat) and prints its tmux window id.
func windowMain(args []string) int {
	takeover := false
	chatID := ""
	for _, a := range args {
		switch {
		case a == "--takeover":
			takeover = true
		case chatIDRe.MatchString(a):
			chatID = a
		default:
			fmt.Fprintln(os.Stderr, "pier-supervisor: bad argument", a)
			return 2
		}
	}
	if exec.Command("tmux", "has-session", "-t", "main").Run() != nil {
		home, _ := os.UserHomeDir()
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", "main", "-c", checkoutDir(home)).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "pier-supervisor: tmux:", strings.TrimSpace(string(out)))
			return 1
		}
		// Its first window is main's home shell (payload.EnsureMain).
		exec.Command("tmux", "set-option", "-w", "-t", "main", "@pier-role", "home").Run()
	}
	home, _ := os.UserHomeDir()
	if takeover {
		if chatID == "" {
			fmt.Fprintln(os.Stderr, "pier-supervisor: --takeover needs a chat")
			return 2
		}
		out, err := exec.Command("tmux", "new-window", "-d", "-t", "main:", "-n", "chat", "-P", "-F", "#{window_id}", "-c", checkoutDir(home),
			"/usr/local/bin/pier-supervisor chat takeover "+chatID+"; echo; echo 'The chat goes on in the app.'; sleep 2").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "pier-supervisor: tmux new-window:", err)
			return 1
		}
		fmt.Print(strings.TrimSpace(string(out)))
		return 0
	}
	if out, err := exec.Command("tmux", "list-windows", "-t", "main", "-F", "#{window_id} #{@pier-role}").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[1] == "agent" {
				if chatID != "" {
					exec.Command("tmux", "respawn-pane", "-k", "-t", f[0], "/usr/local/bin/pier-supervisor chat view "+chatID).Run()
				}
				exec.Command("tmux", "select-window", "-t", f[0]).Run()
				fmt.Print(f[0])
				return 0
			}
		}
	}
	view := "/usr/local/bin/pier-supervisor chat view"
	if chatID != "" {
		view += " " + chatID
	}
	out, err := exec.Command("tmux", "new-window", "-t", "main:", "-n", "chats", "-P", "-F", "#{window_id}", "-c", checkoutDir(home), view).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor: tmux new-window:", err)
		return 1
	}
	win := strings.TrimSpace(string(out))
	exec.Command("tmux", "set-option", "-w", "-t", win, "@pier-role", "agent").Run()
	fmt.Print(win)
	return 0
}

// --- the view ------------------------------------------------------------------

const (
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[2m"
	cItalic = "\x1b[3m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
)

type viewer struct {
	w io.Writer

	mu     sync.Mutex
	conn   net.Conn
	chats  []wire.ChatInfo
	cur    string
	pinned bool // the user chose the chat: don't follow activity
	// printed is each item as last printed; streamed the ones whose text
	// went out as it was written.
	printed  map[string]wire.ChatItem
	streamed map[string]bool
	open     string // the item whose text the cursor sits in
	state    string // the shown chat's state, as last printed
	asked    string
}

func viewMain(chatID string) int {
	v := &viewer{w: os.Stdout, cur: chatID, pinned: chatID != "", printed: map[string]wire.ChatItem{}, streamed: map[string]bool{}}
	restore := cbreak()
	defer restore()
	keys := make(chan byte, 8)
	go func() {
		b := make([]byte, 1)
		for {
			if n, err := os.Stdin.Read(b); err != nil || n == 0 {
				close(keys)
				return
			} else {
				keys <- b[0]
			}
		}
	}()
	for {
		err := v.follow(keys, restore)
		if errors.Is(err, errQuit) {
			return 0
		}
		fmt.Fprintf(v.w, "\n%sthe session's chats aren't reachable (%v) — trying again…%s\n", cDim, err, cReset)
		select {
		case k, ok := <-keys:
			if !ok || k == 'q' {
				return 0
			}
		case <-sleepCh(3):
		}
	}
}

var errQuit = errors.New("quit")

// cbreak reads keys one at a time without echo; the returned func puts the
// terminal back.
func cbreak() func() {
	save, err := sttyOut("-g")
	if err != nil {
		return func() {}
	}
	stty("-icanon", "-echo", "min", "1")
	fmt.Print("\x1b[?25l") // no cursor in a read-only view
	return func() {
		fmt.Print("\x1b[?25h")
		stty(strings.TrimSpace(save))
	}
}

func stty(args ...string) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	cmd.Run()
}

func sttyOut(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return string(out), err
}

func (v *viewer) send(req map[string]any) {
	b, _ := json.Marshal(req)
	v.conn.Write(append(b, '\n'))
}

// follow streams the chats until the connection ends or the user quits.
func (v *viewer) follow(keys <-chan byte, restore func()) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	v.mu.Lock()
	v.conn = conn
	v.mu.Unlock()
	v.send(map[string]any{"op": "subscribe", "topics": []string{"chats"}})
	msgs := make(chan message, 64)
	go func() {
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var m message
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
		close(msgs)
	}()
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return errors.New("the stream ended")
			}
			v.handle(m)
		case k, ok := <-keys:
			if !ok {
				return errQuit
			}
			switch k {
			case 'q':
				return errQuit
			case 'n', 'p':
				v.step(k == 'n')
			case 'r':
				v.show(v.cur)
			case 't':
				v.mu.Lock()
				id := v.cur
				v.mu.Unlock()
				if id == "" {
					continue
				}
				restore()
				fmt.Print("\x1b[H\x1b[2J")
				if err := runTakeover(id); err != nil {
					fmt.Printf("\n%s%v%s\n%spress a key to go back to the chat%s\n", cRed, err, cReset, cDim, cReset)
					cbreak()
					<-keys
				} else {
					cbreak()
				}
				v.show(id)
			}
		}
	}
}

func (v *viewer) handle(m message) {
	switch m.Ev {
	case "chats":
		var t struct {
			Chats []wire.ChatInfo `json:"chats"`
		}
		if json.Unmarshal(m.Data, &t) != nil {
			return
		}
		v.mu.Lock()
		v.chats = t.Chats
		next := v.cur
		if !v.pinned || !v.has(next) {
			next = v.liveliest()
		}
		v.mu.Unlock()
		if next != v.cur || v.state == "" {
			v.show(next)
			return
		}
		v.status()
	case "log":
		var ch logChunk
		if json.Unmarshal(m.Data, &ch) != nil || ch.Chat != v.cur {
			return
		}
		if ch.Reset && len(v.printed) > 0 {
			v.show(v.cur) // the log was compacted: print it again
			return
		}
		for _, line := range strings.Split(ch.Data, "\n") {
			var it wire.ChatItem
			if line != "" && json.Unmarshal([]byte(line), &it) == nil {
				v.item(it)
			}
		}
		if ch.EOF && v.state == "" {
			// Caught up: say what the chat is doing now.
			v.state = "caught-up"
			v.status()
		}
	case "streaming":
		var s struct {
			Chat  string          `json:"chat"`
			Items []wire.ChatItem `json:"items"`
		}
		if json.Unmarshal(m.Data, &s) != nil || s.Chat != v.cur {
			return
		}
		for _, it := range s.Items {
			v.delta(wire.ChatDelta{ID: it.ID, Kind: it.Kind, Text: it.Text})
		}
	case "delta":
		var d struct {
			Chat   string           `json:"chat"`
			Deltas []wire.ChatDelta `json:"deltas"`
		}
		if json.Unmarshal(m.Data, &d) != nil || d.Chat != v.cur {
			return
		}
		for _, x := range d.Deltas {
			v.delta(x)
		}
	}
}

func (v *viewer) has(id string) bool {
	for _, c := range v.chats {
		if c.ID == id {
			return true
		}
	}
	return false
}

// liveliest is the chat to show: one waiting on you, else working, else
// the newest.
func (v *viewer) liveliest() string {
	if len(v.chats) == 0 {
		return ""
	}
	cs := append([]wire.ChatInfo{}, v.chats...)
	rank := func(c wire.ChatInfo) int {
		switch c.State {
		case wire.AgentWaiting:
			return 0
		case wire.AgentWorking:
			return 1
		}
		return 2
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if rank(cs[i]) != rank(cs[j]) {
			return rank(cs[i]) < rank(cs[j])
		}
		return cs[i].Since.After(cs[j].Since)
	})
	return cs[0].ID
}

func (v *viewer) step(forward bool) {
	v.mu.Lock()
	if len(v.chats) == 0 {
		v.mu.Unlock()
		return
	}
	i := 0
	for j, c := range v.chats {
		if c.ID == v.cur {
			i = j
		}
	}
	if forward {
		i = (i + 1) % len(v.chats)
	} else {
		i = (i + len(v.chats) - 1) % len(v.chats)
	}
	v.pinned = true
	id := v.chats[i].ID
	v.mu.Unlock()
	v.show(id)
}

func (v *viewer) clear() {
	v.printed, v.streamed, v.open = map[string]wire.ChatItem{}, map[string]bool{}, ""
	fmt.Fprint(v.w, "\x1b[H\x1b[2J\x1b[3J")
}

// show starts the view of a chat over.
func (v *viewer) show(id string) {
	v.mu.Lock()
	old := v.cur
	v.cur = id
	conn := v.conn
	v.mu.Unlock()
	v.clear()
	v.state, v.asked = "", ""
	if conn != nil && old != "" && old != id {
		v.send(map[string]any{"op": "untail", "args": map[string]any{"chat": old}})
	}
	c := v.info()
	if c == nil {
		fmt.Fprintf(v.w, "%sNo chats in this session yet. Start one in the app — this window shows it as it goes.%s\n", cDim, cReset)
		fmt.Fprintf(v.w, "%sq quit%s\n", cDim, cReset)
		return
	}
	others := ""
	if n := len(v.chats); n > 1 {
		others = fmt.Sprintf(" · %d chats (n/p to switch)", n)
	}
	fmt.Fprintf(v.w, "%s%s%s %s%s · %s%s%s\n", cBold, c.Title, cReset, cDim, c.Agent, nonEmpty(c.Model, "default model"), others, cReset)
	fmt.Fprintf(v.w, "%sread-only — answer in the app, or press t to continue this chat here · q quit%s\n", cDim, cReset)
	if conn != nil {
		v.send(map[string]any{"op": "tail", "args": map[string]any{"chat": id, "file": "", "from": 0}})
	}
}

func (v *viewer) info() *wire.ChatInfo {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.chats {
		if v.chats[i].ID == v.cur {
			c := v.chats[i]
			return &c
		}
	}
	return nil
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

// endLine finishes the line the cursor sits in, if any.
func (v *viewer) endLine() {
	if v.open != "" {
		fmt.Fprintln(v.w)
		v.open = ""
	}
}

func (v *viewer) delta(d wire.ChatDelta) {
	if _, done := v.printed[d.ID]; done {
		return
	}
	if v.open != d.ID {
		v.endLine()
		fmt.Fprintln(v.w)
		if d.Kind == wire.ChatThinking {
			fmt.Fprint(v.w, cDim+cItalic)
		}
		v.open = d.ID
	}
	text := d.Text
	if d.Kind == wire.ChatThinking {
		text += cReset
	}
	fmt.Fprint(v.w, text)
	v.streamed[d.ID] = true
}

func indent(s, prefix string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

func (v *viewer) item(it wire.ChatItem) {
	prev, seen := v.printed[it.ID]
	v.printed[it.ID] = it
	if v.streamed[it.ID] {
		// Its text is on screen already.
		if v.open == it.ID {
			v.endLine()
		}
		return
	}
	switch it.Kind {
	case wire.ChatUser:
		v.endLine()
		fmt.Fprintf(v.w, "\n%s› %s%s\n", cBold, indent(it.Text, "  "), cReset)
		for _, a := range it.Attachments {
			fmt.Fprintf(v.w, "%s  📎 %s%s\n", cDim, a.Name, cReset)
		}
	case wire.ChatAssistant:
		v.endLine()
		fmt.Fprintf(v.w, "\n%s\n", strings.TrimRight(it.Text, "\n"))
	case wire.ChatThinking:
		v.endLine()
		fmt.Fprintf(v.w, "\n%s%s✻ %s%s\n", cDim, cItalic, indent(it.Text, "  "), cReset)
	case wire.ChatPlan:
		v.endLine()
		fmt.Fprintf(v.w, "\n%sPlan%s\n%s\n", cBold, cReset, strings.TrimRight(it.Text, "\n"))
	case wire.ChatStatus:
		v.endLine()
		fmt.Fprintf(v.w, "%s— %s%s\n", cDim, it.Text, cReset)
	case wire.ChatError:
		v.endLine()
		fmt.Fprintf(v.w, "%s! %s%s\n", cRed, indent(it.Text, "  "), cReset)
	case wire.ChatTool:
		v.tool(it, prev, seen)
	}
}

func (v *viewer) tool(it wire.ChatItem, prev wire.ChatItem, seen bool) {
	t := it.Tool
	if t == nil {
		return
	}
	v.endLine()
	if !seen || prev.Tool == nil {
		bullet := cCyan + "●" + cReset
		fmt.Fprintf(v.w, "%s %s%s%s %s\n", bullet, cBold, t.Name, cReset, oneLineOf(t.Summary))
	} else if prev.Tool.Status == t.Status {
		return
	}
	switch t.Status {
	case wire.ToolRunning:
	case wire.ToolOK:
		out := ""
		if t.Bash != nil {
			out = lastLines(t.Bash.Output, 3)
		}
		fmt.Fprintf(v.w, "  %s⎿ ✓%s %s%s%s\n", cGreen, cReset, cDim, indent(out, "      "), cReset)
	case wire.ToolDenied:
		fmt.Fprintf(v.w, "  %s⎿ declined%s\n", cYellow, cReset)
	case wire.ToolInterrupted:
		fmt.Fprintf(v.w, "  %s⎿ interrupted%s\n", cYellow, cReset)
	default:
		out := t.Result
		if t.Bash != nil {
			out = t.Bash.Output
		}
		fmt.Fprintf(v.w, "  %s⎿ ✗ %s%s\n", cRed, indent(lastLines(out, 3), "      "), cReset)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// status prints the shown chat's state when it changes.
func (v *viewer) status() {
	c := v.info()
	if c == nil {
		return
	}
	asked := ""
	if c.Pending != nil {
		asked = c.Pending.ID
	}
	if string(c.State) == v.state && asked == v.asked {
		return
	}
	prev := v.state
	v.state, v.asked = string(c.State), asked
	if prev == "" {
		return // not caught up yet
	}
	v.endLine()
	switch c.State {
	case wire.AgentWaiting:
		what := "an answer"
		if p := c.Pending; p != nil {
			switch {
			case len(p.Questions) > 0:
				what = "answers: " + p.Questions[0].Question
			case p.Kind == "plan":
				what = "a decision on its plan"
			default:
				what = "your approval: " + p.Tool + " " + oneLineOf(p.Summary)
			}
		}
		fmt.Fprintf(v.w, "%s▲ waiting for %s%s %s(answer in the app, or t to continue here)%s\n", cYellow, what, cReset, cDim, cReset)
	case wire.AgentReady:
		if prev == string(wire.AgentWorking) || prev == string(wire.AgentWaiting) {
			fmt.Fprintf(v.w, "%s— done%s\n", cDim, cReset)
		}
	case wire.AgentWorking:
		if prev == "caught-up" {
			fmt.Fprintf(v.w, "%s… working%s\n", cDim, cReset)
		}
	case wire.AgentTerminal:
		fmt.Fprintf(v.w, "%s— going on in a terminal; this chat resumes when it exits%s\n", cDim, cReset)
	case wire.AgentError:
		fmt.Fprintf(v.w, "%s! %s%s\n", cRed, c.Error, cReset)
	}
}

func sleepCh(seconds int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		exec.Command("sleep", fmt.Sprint(seconds)).Run()
		close(ch)
	}()
	return ch
}

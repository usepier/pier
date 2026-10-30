package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// The supervisor serves a local socket so a frontend can follow a session
// live and change things in it, over one ssh channel: `pier-supervisor
// stream` relays the socket to its stdio. Following never counts as
// attached (no tmux client, no forwarded TCP), so watching a session never
// keeps it awake.
//
// Messages are JSON lines. The supervisor greets with hello, then pushes the
// topics a client subscribed to. Changes a client asks for (ops) run here,
// in the supervisor, not in the ssh channel that asked: a dropped connection
// can't leave one half done, and an op's id makes a retry a no-op.

// sockPath is where the supervisor listens; a var so tests listen elsewhere.
var sockPath = "/run/pier/supervisor.sock"

const (
	// streamProto goes up with every change to the stream protocol; a
	// frontend that needs newer replaces the supervisor.
	streamProto = 5
)

type message struct {
	Ev  string `json:"ev,omitempty"`
	ID  string `json:"id,omitempty"`
	OK  *bool  `json:"ok,omitempty"`
	Err string `json:"error,omitempty"`
	// Code tells failures apart (codeConflict, codeNoCheckout, ...).
	Code string          `json:"code,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type request struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Topics []string        `json:"topics"`
	Args   json.RawMessage `json:"args"`
}

type hub struct {
	mu     sync.Mutex
	subs   map[*client]bool
	status json.RawMessage
	env    json.RawMessage
	chats  json.RawMessage
	// git is the checkout's changes as last published; gitLive says the
	// watcher keeps it current, gitKick asks it for a fresh one.
	git     json.RawMessage
	gitLive bool
	gitKick chan struct{}
	results []result // recent op results, oldest first: a replayed id gets its answer
	running map[string]chan struct{}
	boot    string
	// host runs the chats a client can tail.
	host *chatHost
	// deltaMu orders the text being written against the snapshot of it a
	// client gets when it starts following a chat.
	deltaMu sync.Mutex
}

type result struct {
	id   string
	data json.RawMessage
	err  error
}

type client struct {
	mu     sync.Mutex
	w      *bufio.Writer
	topics map[string]bool
	// tails are the chats it follows: each closes to stop its tail.
	tails map[string]chan struct{}
	// searches are its searches running, by id.
	searches map[string]*searchRun
}

type searchRun struct{ cancel context.CancelFunc }

func (c *client) send(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w.Write(b)
	c.w.WriteByte('\n')
	return c.w.Flush()
}

// selfBuild names this supervisor binary (a hash of it): a frontend whose
// pier carries another replaces it once no chat is mid-turn.
var selfBuild = sync.OnceValue(func() string {
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
})

func newHub() *hub {
	return &hub{subs: map[*client]bool{}, running: map[string]chan struct{}{}, boot: bootID(), gitKick: make(chan struct{}, 1)}
}

// bootID names this boot of the machine; a var so tests can reboot.
var bootID = func() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// serve accepts frontends on the socket until the process ends.
func (h *hub) serve() error {
	os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	os.Chmod(sockPath, 0o600)
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go h.handle(conn)
	}
}

func (h *hub) handle(conn net.Conn) {
	defer conn.Close()
	c := &client{w: bufio.NewWriter(conn), topics: map[string]bool{}, tails: map[string]chan struct{}{}, searches: map[string]*searchRun{}}
	hello, _ := json.Marshal(map[string]any{"proto": streamProto, "boot": h.boot, "build": selfBuild()})
	if c.send(message{Ev: "hello", Data: hello}) != nil {
		return
	}
	h.mu.Lock()
	h.subs[c] = true
	h.mu.Unlock()
	defer func() {
		c.untailAll()
		c.cancelSearches()
		h.mu.Lock()
		delete(h.subs, c)
		h.mu.Unlock()
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var r request
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		switch r.Op {
		case "subscribe":
			c.mu.Lock()
			for _, t := range r.Topics {
				c.topics[t] = true
			}
			c.mu.Unlock()
			h.mu.Lock()
			status, env, chats := h.status, h.env, h.chats
			git := h.git
			if !h.gitLive {
				git = nil
			}
			h.mu.Unlock()
			if c.wants("status") && status != nil {
				c.send(message{Ev: "status", Data: status})
			}
			if c.wants("chats") && chats != nil {
				c.send(message{Ev: "chats", Data: chats})
			}
			if c.wants("env") {
				if env == nil {
					env = envSnapshot(nil)
				}
				c.send(message{Ev: "env", Data: withLoad(env)})
			}
			if c.wants("git") {
				// A watcher that kept it current answers now; otherwise
				// the first read goes to every follower.
				if git != nil {
					c.send(message{Ev: "git", Data: git})
				} else {
					h.kickGit()
				}
			}
		case "ping":
			c.send(message{Ev: "pong", ID: r.ID})
		case "tail":
			// Send a chat's log from where the client's copy ends:
			// {"chat": id, "file": the log's name, "from": its length},
			// then the text being written as it streams.
			var a struct {
				Chat string `json:"chat"`
				File string `json:"file"`
				From int64  `json:"from"`
			}
			json.Unmarshal(r.Args, &a)
			var l *chatLog
			if h.host != nil {
				l = h.host.logOf(a.Chat)
			}
			if l == nil {
				c.send(message{Ev: "log", Data: mustJSON(logChunk{Chat: a.Chat, EOF: true, Reset: true})})
				continue
			}
			c.untail(a.Chat)
			stop := make(chan struct{})
			c.mu.Lock()
			c.tails[a.Chat] = stop
			c.mu.Unlock()
			chatID := a.Chat
			go tailLog(c, chatID, l, a.File, max(a.From, 0), stop, func() { h.sendStreaming(c, chatID) })
		case "untail":
			var a struct {
				Chat string `json:"chat"`
			}
			json.Unmarshal(r.Args, &a)
			c.untail(a.Chat)
		case "search":
			// Search the checkout, sending matches as they're found:
			// "search" messages with the request's id until one says done.
			var a searchArgs
			json.Unmarshal(r.Args, &a)
			a.ID = r.ID
			ctx, cancel := context.WithCancel(context.Background())
			run := &searchRun{cancel: cancel}
			c.mu.Lock()
			if prev := c.searches[r.ID]; prev != nil {
				prev.cancel()
			}
			c.searches[r.ID] = run
			c.mu.Unlock()
			go func() {
				defer c.endSearch(r.ID, run)
				h.search(ctx, a, func(ms []searchMatch, done, truncated bool, err error) {
					c.send(searchMessage(r.ID, ms, done, truncated, err))
				})
			}()
		case "search.cancel":
			c.mu.Lock()
			if run := c.searches[r.ID]; run != nil {
				run.cancel()
			}
			c.mu.Unlock()
		default:
			go func() {
				data, err := h.op(r)
				ok := err == nil
				m := message{Ev: "result", ID: r.ID, OK: &ok, Data: data}
				if err != nil {
					m.Err, m.Code = err.Error(), errCode(err)
				}
				c.send(m)
			}()
		}
	}
}

func (c *client) untail(chatID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stop := c.tails[chatID]; stop != nil {
		close(stop)
		delete(c.tails, chatID)
	}
}

func (c *client) untailAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, stop := range c.tails {
		close(stop)
		delete(c.tails, id)
	}
}

func (c *client) cancelSearches() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, run := range c.searches {
		run.cancel()
		delete(c.searches, id)
	}
}

// endSearch forgets a search that ended, unless another took its id.
func (c *client) endSearch(id string, run *searchRun) {
	run.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.searches[id] == run {
		delete(c.searches, id)
	}
}

func (c *client) tailing(chatID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tails[chatID] != nil
}

// sendStreaming tells a client that caught up on a chat's log what the
// agent is writing right now.
func (h *hub) sendStreaming(c *client, chatID string) {
	h.deltaMu.Lock()
	defer h.deltaMu.Unlock()
	items := h.host.streamingItems(chatID)
	c.send(message{Ev: "streaming", Data: mustJSON(map[string]any{"chat": chatID, "items": items})})
}

// deltas sends text the agent wrote to the clients following its chat.
func (h *hub) deltas(chatID string, ds []wire.ChatDelta) {
	h.deltaMu.Lock()
	defer h.deltaMu.Unlock()
	h.mu.Lock()
	var to []*client
	for c := range h.subs {
		if c.tailing(chatID) {
			to = append(to, c)
		}
	}
	h.mu.Unlock()
	if len(to) == 0 {
		return
	}
	data := mustJSON(map[string]any{"chat": chatID, "deltas": ds})
	for _, c := range to {
		c.send(message{Ev: "delta", Data: data})
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (c *client) wants(topic string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topics[topic]
}

// watching reports whether anyone follows a topic — the expensive samples
// (processes) are only taken then.
func (h *hub) watching(topic string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		if c.wants(topic) {
			return true
		}
	}
	return false
}

// publish sends a topic's new data to its subscribers and keeps it for
// the next one to subscribe.
func (h *hub) publish(topic string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	switch topic {
	case "status":
		h.status = b
	case "env":
		h.env = b
	case "chats":
		h.chats = b
	case "git":
		h.git = b
	}
	var to []*client
	for c := range h.subs {
		if c.wants(topic) {
			to = append(to, c)
		}
	}
	h.mu.Unlock()
	for _, c := range to {
		c.send(message{Ev: topic, Data: b})
	}
}

// op runs one change, once per id: a retry of an id that already ran (or
// is running) gets the first run's answer. Queries just run: asking twice
// changes nothing, and their answers can be large.
func (h *hub) op(r request) (json.RawMessage, error) {
	if q, ok := queries[r.Op]; ok {
		return marshalled(q(r.Args))
	}
	if r.ID == "" {
		return nil, errors.New("an op needs an id")
	}
	for {
		h.mu.Lock()
		for _, res := range h.results {
			if res.id == r.ID {
				h.mu.Unlock()
				return res.data, res.err
			}
		}
		if wait, ok := h.running[r.ID]; ok {
			h.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		h.running[r.ID] = done
		h.mu.Unlock()

		data, err := runOp(r.Op, r.Args)
		h.mu.Lock()
		delete(h.running, r.ID)
		h.results = append(h.results, result{id: r.ID, data: data, err: err})
		if len(h.results) > 256 {
			h.results = h.results[len(h.results)-256:]
		}
		h.mu.Unlock()
		close(done)
		return data, err
	}
}

// ops are the changes frontends can ask for, by name. Chat ops are added
// when the supervisor's chat host starts.
var ops = map[string]func(args json.RawMessage) (any, error){
	"container": containerOp,
	// version says which stream protocol this supervisor speaks; one that
	// doesn't know the op is older than any that does.
	"version": func(json.RawMessage) (any, error) { return map[string]int{"proto": streamProto}, nil },
}

// queries read the session for frontends, by name.
var queries = map[string]func(args json.RawMessage) (any, error){}

func runOp(name string, args json.RawMessage) (json.RawMessage, error) {
	f, ok := ops[name]
	if !ok {
		return nil, fmt.Errorf("unknown op %q", name)
	}
	return marshalled(f(args))
}

func marshalled(v any, err error) (json.RawMessage, error) {
	if err != nil || v == nil {
		return nil, err
	}
	return json.Marshal(v)
}

var containerRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func containerOp(raw json.RawMessage) (any, error) {
	var a struct{ Action, Container string }
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	switch a.Action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("unknown container action %q", a.Action)
	}
	if !containerRef.MatchString(a.Container) {
		return nil, fmt.Errorf("bad container %q", a.Container)
	}
	out, err := exec.Command("docker", a.Action, a.Container).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %s", a.Action, strings.TrimSpace(string(out)))
	}
	return nil, nil
}

// envState is what runs in the session, for the environment view. Windows
// and containers go as their tools print them; the laptop parses them.
type envState struct {
	Windows    string     `json:"windows"`
	Containers string     `json:"containers"`
	Processes  []proc     `json:"processes"`
	Ports      []port     `json:"ports"`
	Res        *resources `json:"res,omitempty"`
	At         time.Time  `json:"at"`
}

// windowFormat matches the laptop's parser (pkg/pier).
const windowFormat = "#{window_id}|#{window_index}|#{window_active}|#{pane_current_command}|#{@pier-role}|#{window_name}"

func envSnapshot(sampler *procSampler) json.RawMessage {
	var e envState
	e.At = time.Now()
	if out, err := exec.Command("tmux", "list-windows", "-t", "main", "-F", windowFormat).Output(); err == nil {
		e.Windows = string(out)
	}
	if out, err := docker("ps", "-a", "--format", "{{json .}}"); err == nil {
		e.Containers = string(out)
	}
	if sampler != nil {
		e.Processes = sampler.top(8)
	}
	b, _ := json.Marshal(e)
	return b
}

// streamMain relays the socket to stdio: `pier-supervisor stream`, run over
// ssh. Exit 3 means no socket — an older supervisor, or one restarting.
func streamMain() int {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor: no stream socket:", err)
		return 3
	}
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(conn, os.Stdin)
		if uc, ok := conn.(*net.UnixConn); ok {
			uc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()
	<-done
	return 0
}

// opMain runs one op and prints its result: `pier-supervisor op <json>`.
// Exit 3 means no socket; 1 the op failed (the reason on stderr).
func opMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: pier-supervisor op <json>")
		return 2
	}
	out, code := opRequest(args[0])
	os.Stdout.Write(out)
	return code
}

// opRequest sends one op request to the supervisor and returns its data
// and an exit code: 0 done, 1 failed (reason on stderr), 2 malformed,
// 3 no supervisor socket.
func opRequest(req string) ([]byte, int) {
	var r request
	if err := json.Unmarshal([]byte(req), &r); err != nil || r.ID == "" {
		fmt.Fprintln(os.Stderr, "pier-supervisor: an op is JSON with an id")
		return nil, 2
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pier-supervisor: no stream socket:", err)
		return nil, 3
	}
	defer conn.Close()
	conn.Write(append([]byte(req), '\n'))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Ev != "result" || m.ID != r.ID {
			continue
		}
		if m.OK == nil || !*m.OK {
			// The code goes on a line of its own, for the frontend to
			// tell the failure apart.
			fmt.Fprintln(os.Stderr, m.Err)
			if m.Code != "" {
				fmt.Fprintln(os.Stderr, "pier-op-code: "+m.Code)
			}
			return nil, 1
		}
		return m.Data, 0
	}
	fmt.Fprintln(os.Stderr, "pier-supervisor: the supervisor went away mid-op")
	return nil, 1
}

package main

import (
	"bufio"
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
	// streamProto changes when the stream protocol breaks compatibility; a
	// frontend that needs newer replaces the supervisor.
	streamProto = 1
)

type message struct {
	Ev   string          `json:"ev,omitempty"`
	ID   string          `json:"id,omitempty"`
	OK   *bool           `json:"ok,omitempty"`
	Err  string          `json:"error,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type request struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Topics []string        `json:"topics"`
	Args   json.RawMessage `json:"args"`
}

type hub struct {
	mu      sync.Mutex
	subs    map[*client]bool
	status  json.RawMessage
	env     json.RawMessage
	results []result // recent op results, oldest first: a replayed id gets its answer
	running map[string]chan struct{}
	boot    string
	agent   json.RawMessage
	// onLastApprover runs when the last frontend able to answer
	// permission requests disconnects.
	onLastApprover func()
}

type result struct {
	id   string
	data json.RawMessage
	err  string
}

type client struct {
	mu     sync.Mutex
	w      *bufio.Writer
	topics map[string]bool
}

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

func newHub() *hub {
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return &hub{subs: map[*client]bool{}, running: map[string]chan struct{}{}, boot: strings.TrimSpace(string(boot))}
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
	c := &client{w: bufio.NewWriter(conn), topics: map[string]bool{}}
	hello, _ := json.Marshal(map[string]any{"proto": streamProto, "boot": h.boot})
	if c.send(message{Ev: "hello", Data: hello}) != nil {
		return
	}
	h.mu.Lock()
	h.subs[c] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, c)
		h.mu.Unlock()
		if c.wants("approvals") && !h.answerable() && h.onLastApprover != nil {
			h.onLastApprover()
		}
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
			status, env, agent := h.status, h.env, h.agent
			h.mu.Unlock()
			if c.wants("status") && status != nil {
				c.send(message{Ev: "status", Data: status})
			}
			if c.wants("agent") && agent != nil {
				c.send(message{Ev: "agent", Data: agent})
			}
			if c.wants("env") {
				if env == nil {
					env = envSnapshot(nil)
				}
				c.send(message{Ev: "env", Data: env})
			}
		case "ping":
			c.send(message{Ev: "pong", ID: r.ID})
		default:
			go func() {
				data, err := h.op(r)
				ok := err == nil
				m := message{Ev: "result", ID: r.ID, OK: &ok, Data: data}
				if err != nil {
					m.Err = err.Error()
				}
				c.send(m)
			}()
		}
	}
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
	case "agent":
		h.agent = b
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
// is running) gets the first run's answer.
func (h *hub) op(r request) (json.RawMessage, error) {
	if r.ID == "" {
		return nil, errors.New("an op needs an id")
	}
	for {
		h.mu.Lock()
		for _, res := range h.results {
			if res.id == r.ID {
				h.mu.Unlock()
				if res.err != "" {
					return res.data, errors.New(res.err)
				}
				return res.data, nil
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
		res := result{id: r.ID, data: data}
		if err != nil {
			res.err = err.Error()
		}
		h.mu.Lock()
		delete(h.running, r.ID)
		h.results = append(h.results, res)
		if len(h.results) > 256 {
			h.results = h.results[len(h.results)-256:]
		}
		h.mu.Unlock()
		close(done)
		return data, err
	}
}

// ops are the changes frontends (and the agent's hooks) can ask for, by
// name. Agent ops are added when the supervisor's agent tracker starts.
var ops = map[string]func(args json.RawMessage) (any, error){
	"container": containerOp,
}

// answerable reports whether a connected frontend can answer permission
// requests (it subscribed to "approvals").
func (h *hub) answerable() bool { return h.watching("approvals") }

func runOp(name string, args json.RawMessage) (json.RawMessage, error) {
	f, ok := ops[name]
	if !ok {
		return nil, fmt.Errorf("unknown op %q", name)
	}
	v, err := f(args)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
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
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Ev != "result" || m.ID != r.ID {
			continue
		}
		if m.OK == nil || !*m.OK {
			fmt.Fprintln(os.Stderr, m.Err)
			return nil, 1
		}
		return m.Data, 0
	}
	fmt.Fprintln(os.Stderr, "pier-supervisor: the supervisor went away mid-op")
	return nil, 1
}

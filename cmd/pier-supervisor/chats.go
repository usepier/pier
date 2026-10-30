package main

// The session's chats: conversations with agents (claude, codex) that the
// supervisor runs headless, as its own children, speaking their programs'
// protocols. A chat is a directory, ~/.pier/chats/<id>:
//
//	chat.json        what it is: agent, model, effort, mode, conversation
//	log-<gen>.jsonl  its items (chatlog.go)
//	queue/<id>.json  prompts waiting for the agent's turn to end
//	sent/<id>.json   prompts delivered (so a retried send is a no-op)
//	agent.log        what its program wrote to stderr
//
// The conversation itself lives where the agent keeps it (claude's
// transcript, codex's rollout), so a program that stopped — idle for a
// while, the session parked — resumes it on the next prompt. Frontends
// follow the chats over the stream and answer what the agents ask (tool
// approvals, questions, plans), which waits here for as long as it takes;
// a session waiting on its user still parks when idle, and its chat says
// what it was waiting for.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/usepier/pier/internal/chatfile"
	"github.com/usepier/pier/pkg/pier/wire"
)

const (
	// idleStop is how long an agent program waits for a prompt before it
	// is stopped (the conversation resumes on the next one).
	idleStop = 20 * time.Minute
	// Failures: a program that keeps dying puts its chat in the error
	// state rather than restarting forever.
	failLimit  = 3
	failWindow = 10 * time.Minute
)

var chatIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func chatsDir(home string) string { return filepath.Join(home, chatfile.Root) }

// chatRecord is a chat's chat.json; prompt one of its prompts.
type (
	chatRecord = chatfile.Record
	prompt     = chatfile.Prompt
)

// decision is a frontend's answer to what an agent asks.
type decision struct {
	Behavior string // allow | allowSession | deny
	Message  string
	Answers  map[string]string
	Notes    map[string]string
	Mode     string
}

// pendingReq is something an agent waits on a frontend for.
type pendingReq struct {
	req wire.PermissionRequest
	// answer tells the agent; it runs without the host's lock.
	answer func(decision) error
}

// agentProc is a chat's running agent program.
type agentProc interface {
	// prompt starts a turn with p, or (steer) adds p to the running one.
	prompt(p prompt, steer bool) error
	interrupt() error
	// configure applies a changed model, effort or mode to the running
	// program (the next start reads them from the record anyway).
	configure(rec chatRecord) error
	// stop ends the program; the conversation stays on disk.
	stop()
	alive() bool
}

type chatState struct {
	rec      chatRecord
	dir      string
	log      *chatLog
	state    wire.AgentState
	waiting  string
	since    time.Time
	activity time.Time
	pending  []*pendingReq
	// streaming are the items being written, as far as they got.
	streaming map[string]*wire.ChatItem
	rt        agentProc
	failures  []time.Time
	// starting: a program start is under way (outside the lock).
	starting bool
	// fastOff is why the agent's fast mode is off though asked for (its
	// own reason: "preference" is the organization's setting), as it last
	// said.
	fastOff string
}

// chatHost runs the session's chats.
type chatHost struct {
	mu sync.Mutex
	// loading lets one load run at a time: two at once (a create's, the
	// tick's) would each open a new chat, the second taking a turn the
	// first had started for one the session stopped.
	loading sync.Mutex
	home    string
	chats   map[string]*chatState
	// notify publishes the chats' state; deltas the text being written.
	notify func()
	deltas func(chatID string, ds []wire.ChatDelta)
	pins   *pinned
	// program finds an agent's program (pinned, else installed); a field
	// so tests run fakes.
	program func(name string) (path, note string)
	codex   *codexServer
	// setupRunning: the repo's setup still runs (first prompts wait).
	setupRunning func() bool
	saveMu       sync.Mutex
	// naming: the branch is being named after a first prompt (branchname.go).
	naming bool
}

func newChatHost(home string) *chatHost {
	h := &chatHost{home: home, chats: map[string]*chatState{}, notify: func() {}, deltas: func(string, []wire.ChatDelta) {}, pins: newPinned(home), setupRunning: func() bool { return false }}
	h.program = h.pins.program
	return h
}

// load reads every chat on disk not yet loaded: at start, and on each tick
// (a create or claim drops new ones in).
func (h *chatHost) load() {
	h.loading.Lock()
	defer h.loading.Unlock()
	ents, _ := os.ReadDir(chatsDir(h.home))
	changed := false
	for _, e := range ents {
		if !e.IsDir() || !chatIDRe.MatchString(e.Name()) {
			continue
		}
		h.mu.Lock()
		_, have := h.chats[e.Name()]
		h.mu.Unlock()
		if have {
			continue
		}
		c, err := h.open(e.Name())
		if err != nil {
			fmt.Println("pier-supervisor: chat", e.Name()+":", err)
			continue
		}
		h.mu.Lock()
		h.chats[c.rec.ID] = c
		h.mu.Unlock()
		changed = true
	}
	if changed {
		h.notify()
	}
}

func (h *chatHost) open(id string) (*chatState, error) {
	dir := filepath.Join(chatsDir(h.home), id)
	b, err := os.ReadFile(filepath.Join(dir, "chat.json"))
	if err != nil {
		return nil, err
	}
	var rec chatRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	if rec.ID != id || (rec.Agent != "claude" && rec.Agent != "codex") {
		return nil, errors.New("unreadable chat.json")
	}
	l, err := openChatLog(dir, rec.Log)
	if err != nil {
		return nil, err
	}
	if err := l.compact(); err != nil {
		fmt.Println("pier-supervisor: compacting chat", id+":", err)
	}
	c := &chatState{rec: rec, dir: dir, log: l, state: wire.AgentReady, since: time.Now(), activity: time.Now(), streaming: map[string]*wire.ChatItem{}}
	if rec.Created.IsZero() {
		c.rec.Created = time.Now()
	}
	if rec.Agent == "claude" && rec.Conversation == "" {
		c.rec.Conversation = newUUID() // a chat seeded by a create
	}
	// Whatever ran when the supervisor last stopped is gone with it.
	switch rec.State {
	case wire.AgentWorking:
		c.log.put(cutOff(l)...)
		c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: "interrupted when the session stopped"})
	case wire.AgentWaiting:
		c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: "the session stopped while waiting for your answer — send a message to go on"})
	case wire.AgentTerminal:
		c.state = wire.AgentTerminal // the terminal take-over may still run
	case wire.AgentError:
		c.state = wire.AgentError
	}
	c.rec.Log = filepath.Base(l.path())
	h.save(c)
	return c, nil
}

// cutOff is a log's tool calls still running, marked interrupted: their
// program is gone.
func cutOff(l *chatLog) []wire.ChatItem {
	items, _, err := readItems(l.path())
	if err != nil {
		return nil
	}
	var out []wire.ChatItem
	for _, it := range items {
		if it.Tool != nil && it.Tool.Status == wire.ToolRunning {
			t := *it.Tool
			t.Status = wire.ToolInterrupted
			it.Tool = &t
			out = append(out, it)
		}
	}
	return out
}

// save writes a chat's record. Called with mu held or before the chat is
// shared.
func (h *chatHost) save(c *chatState) {
	c.rec.State, c.rec.Waiting = c.state, c.waiting
	b, _ := json.MarshalIndent(c.rec, "", "  ")
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if err := writeAtomic(filepath.Join(c.dir, "chat.json"), b); err != nil {
		fmt.Println("pier-supervisor: saving chat", c.rec.ID+":", err)
	}
}

// setState moves a chat to a state. Called with mu held.
func (h *chatHost) setState(c *chatState, s wire.AgentState, waiting string) {
	if c.state != s || c.waiting != waiting {
		c.since = time.Now()
	}
	c.state, c.waiting, c.activity = s, waiting, time.Now()
	h.save(c)
}

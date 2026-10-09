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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/internal/chatfile"
	"github.com/usepier/pier/pkg/pier/chat"
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
}

// chatHost runs the session's chats.
type chatHost struct {
	mu    sync.Mutex
	home  string
	chats map[string]*chatState
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
}

func newChatHost(home string) *chatHost {
	h := &chatHost{home: home, chats: map[string]*chatState{}, notify: func() {}, deltas: func(string, []wire.ChatDelta) {}, pins: newPinned(home), setupRunning: func() bool { return false }}
	h.program = h.pins.program
	return h
}

// load reads every chat on disk not yet loaded: at start, and on each tick
// (a create or claim drops new ones in).
func (h *chatHost) load() {
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

// --- creating and sending --------------------------------------------------

type createArgs struct {
	ID     string  `json:"id"`
	Agent  string  `json:"agent"`
	Model  string  `json:"model"`
	Effort string  `json:"effort"`
	Mode   string  `json:"mode"`
	Title  string  `json:"title"`
	Prompt *prompt `json:"prompt"`
}

// create makes a chat, once per id.
func (h *chatHost) create(a createArgs) error {
	if !chatIDRe.MatchString(a.ID) {
		return fmt.Errorf("bad chat id %q", a.ID)
	}
	if a.Agent != "claude" && a.Agent != "codex" {
		return fmt.Errorf("unknown agent %q (claude or codex)", a.Agent)
	}
	if err := checkMode(a.Agent, a.Mode); err != nil {
		return err
	}
	h.mu.Lock()
	_, have := h.chats[a.ID]
	h.mu.Unlock()
	dir := filepath.Join(chatsDir(h.home), a.ID)
	if _, err := os.Stat(dir); have || err == nil {
		if a.Prompt != nil {
			return h.send(a.ID, *a.Prompt, "queue")
		}
		return nil
	}
	if err := writeChat(h.home, a); err != nil {
		return err
	}
	h.load()
	return nil
}

// writeChat lays a new chat out on disk, its first prompt queued.
func writeChat(home string, a createArgs) error {
	dir := filepath.Join(chatsDir(home), a.ID)
	if err := os.MkdirAll(filepath.Join(dir, "queue"), 0o700); err != nil {
		return err
	}
	rec := chatRecord{ID: a.ID, Agent: a.Agent, Title: a.Title, Model: a.Model, Effort: a.Effort, Mode: a.Mode, Created: time.Now()}
	if rec.Agent == "claude" {
		rec.Conversation = newUUID()
	}
	if rec.Title == "" && a.Prompt != nil {
		rec.Title = titleOf(a.Prompt.Text)
	}
	if a.Prompt != nil {
		if err := enqueue(dir, *a.Prompt); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return writeAtomic(filepath.Join(dir, "chat.json"), b)
}

// titleOf names a chat after its first prompt.
func titleOf(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	if r := []rune(t); len(r) > 48 {
		t = strings.TrimSpace(string(r[:47])) + "…"
	}
	return t
}

var promptID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// enqueue adds a prompt to a chat's queue, once per id.
func enqueue(dir string, p prompt) error {
	if !promptID.MatchString(p.ID) {
		return fmt.Errorf("bad prompt id %q", p.ID)
	}
	for _, sub := range []string{"queue", "sent"} {
		if _, err := os.Stat(filepath.Join(dir, sub, p.ID+".json")); err == nil {
			return nil // already queued or delivered
		}
	}
	if p.At.IsZero() {
		p.At = time.Now()
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "queue"), 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "queue", p.ID+".json"), b)
}

func queued(dir string) []prompt {
	files, _ := filepath.Glob(filepath.Join(dir, "queue", "*.json"))
	var out []prompt
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p prompt
		if json.Unmarshal(b, &p) == nil && p.ID != "" {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func sent(dir string, id string) bool {
	_, err := os.Stat(filepath.Join(dir, "sent", id+".json"))
	return err == nil
}

func markSent(dir string, p prompt) {
	_ = os.MkdirAll(filepath.Join(dir, "sent"), 0o700)
	src := filepath.Join(dir, "queue", p.ID+".json")
	dst := filepath.Join(dir, "sent", p.ID+".json")
	if os.Rename(src, dst) != nil {
		b, _ := json.Marshal(p)
		_ = writeAtomic(dst, b)
	}
}

func dropQueued(dir, id string) bool {
	return os.Remove(filepath.Join(dir, "queue", id+".json")) == nil
}

// send gives a chat a prompt: at once when it is free (or, steering, into
// the running turn), else queued for when its turn ends.
func (h *chatHost) send(chatID string, p prompt, mode string) error {
	if strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0 {
		return errors.New("an empty prompt")
	}
	h.mu.Lock()
	c := h.chats[chatID]
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("no chat %s", chatID)
	}
	dir := c.dir
	h.mu.Unlock()
	if sent(dir, p.ID) {
		return nil
	}
	if err := enqueue(dir, p); err != nil {
		return err
	}
	if mode == "steer" {
		h.mu.Lock()
		working := c.state == wire.AgentWorking && c.rt != nil && c.rt.alive()
		h.mu.Unlock()
		if working {
			return h.deliverOne(c, p, true)
		}
	}
	h.notify()
	h.deliver(c)
	return nil
}

// deliver starts the chat's next queued prompt if it is free for one.
func (h *chatHost) deliver(c *chatState) {
	h.mu.Lock()
	free := (c.state == wire.AgentReady) && !c.starting
	dir := c.dir
	h.mu.Unlock()
	if !free {
		return
	}
	q := queued(dir)
	if len(q) == 0 {
		return
	}
	p := q[0]
	if p.First && h.setupRunning() {
		return
	}
	if err := h.deliverOne(c, p, false); err != nil {
		fmt.Println("pier-supervisor: chat", c.rec.ID+":", err)
	}
}

// deliverAll gives every free chat its next prompt: the supervisor's loop
// runs it each second (setup finishing frees first prompts).
func (h *chatHost) deliverAll() {
	h.mu.Lock()
	var cs []*chatState
	for _, c := range h.chats {
		cs = append(cs, c)
	}
	h.mu.Unlock()
	for _, c := range cs {
		h.deliver(c)
	}
}

// deliverOne hands a prompt to the chat's program, starting it if need be.
func (h *chatHost) deliverOne(c *chatState, p prompt, steer bool) error {
	h.mu.Lock()
	if !steer {
		if c.state != wire.AgentReady || c.starting {
			h.mu.Unlock()
			return nil
		}
		c.starting = true
		h.setState(c, wire.AgentWorking, "")
		c.rec.Error = ""
	}
	h.mu.Unlock()
	h.notify()

	rt, err := h.proc(c)
	if err == nil {
		item := wire.ChatItem{ID: p.ID, Kind: wire.ChatUser, Time: time.Now(), Text: p.Text, Attachments: p.Attachments}
		c.log.put(item)
		markSent(c.dir, p)
		err = rt.prompt(p, steer)
	}
	h.mu.Lock()
	if !steer {
		c.starting = false
	}
	if err != nil {
		c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatError, Time: time.Now(), Text: "Couldn't start the turn: " + err.Error()})
		c.rec.Error = err.Error()
		h.failed(c)
	}
	h.mu.Unlock()
	h.notify()
	return err
}

// proc is the chat's running program, started if it isn't.
func (h *chatHost) proc(c *chatState) (agentProc, error) {
	h.mu.Lock()
	rt := c.rt
	h.mu.Unlock()
	if rt != nil && rt.alive() {
		return rt, nil
	}
	var err error
	switch c.rec.Agent {
	case "codex":
		rt, err = h.codexServer().thread(c)
	default:
		rt, err = startClaude(h, c)
	}
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	c.rt = rt
	h.mu.Unlock()
	return rt, nil
}

// failed records a program failure; too many in a row stop the chat.
// Called with mu held.
func (h *chatHost) failed(c *chatState) {
	now := time.Now()
	var recent []time.Time
	for _, t := range append(c.failures, now) {
		if now.Sub(t) < failWindow {
			recent = append(recent, t)
		}
	}
	c.failures = recent
	if len(recent) >= failLimit {
		h.setState(c, wire.AgentError, "")
		return
	}
	h.setState(c, wire.AgentReady, "")
}

// --- what the agents report ------------------------------------------------

// apply records what an agent's stream says. It runs on the program's
// reader goroutine.
func (h *chatHost) apply(c *chatState, ev chat.Event) {
	var items []wire.ChatItem
	for _, ch := range ev.Changes {
		items = append(items, ch.Item)
	}
	if len(items) > 0 {
		if err := c.log.put(items...); err != nil {
			fmt.Println("pier-supervisor: chat log:", err)
		}
	}
	h.mu.Lock()
	for _, it := range items {
		delete(c.streaming, it.ID)
		if it.Kind == wire.ChatAssistant && !it.Sidechain {
			c.rec.Last = truncate(it.Text, 400)
		}
	}
	for _, d := range ev.Deltas {
		s := c.streaming[d.ID]
		if s == nil {
			s = &wire.ChatItem{ID: d.ID, Kind: d.Kind, Time: time.Now()}
			c.streaming[d.ID] = s
		}
		s.Text += d.Text
	}
	if ev.Session != "" && c.rec.Conversation != ev.Session {
		c.rec.Conversation = ev.Session
	}
	if ev.Session != "" && !c.rec.Started {
		c.rec.Started = true
	}
	c.activity = time.Now()
	ended := ev.TurnEnd != nil
	if ended {
		c.streaming = map[string]*wire.ChatItem{}
		c.pending = nil
		if ev.TurnEnd.Error != "" {
			c.rec.Error = ev.TurnEnd.Error
		}
		if c.state == wire.AgentWorking || c.state == wire.AgentWaiting {
			h.setState(c, wire.AgentReady, "")
		} else {
			h.save(c)
		}
	}
	h.mu.Unlock()
	if len(ev.Deltas) > 0 {
		h.deltas(c.rec.ID, ev.Deltas)
	}
	if ended || len(items) > 0 {
		h.notify()
	}
	if ended {
		go h.deliver(c)
	}
}

// exited records a program's end. Called by the program's waiter.
func (h *chatHost) exited(c *chatState, rt agentProc, why string) {
	h.mu.Lock()
	if c.rt == rt {
		c.rt = nil
	}
	cut := c.state == wire.AgentWorking || c.state == wire.AgentWaiting
	c.pending = nil
	c.streaming = map[string]*wire.ChatItem{}
	if cut {
		text := "The agent stopped unexpectedly."
		if why != "" {
			text += "\n\n" + why
		}
		c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatError, Time: time.Now(), Text: text})
		c.rec.Error = text
		h.failed(c)
	}
	h.mu.Unlock()
	h.notify()
}

// ask records something the agent waits on a frontend for.
func (h *chatHost) ask(c *chatState, p *pendingReq) {
	h.mu.Lock()
	p.req.At = time.Now()
	c.pending = append(c.pending, p)
	h.setState(c, wire.AgentWaiting, waitingFor(c.pending[0]))
	h.mu.Unlock()
	h.notify()
}

// endTurnAsking ends a turn with a question to the user (a plan to
// approve): waiting, not free for the next prompt.
func (h *chatHost) endTurnAsking(c *chatState, p *pendingReq) {
	h.mu.Lock()
	c.streaming = map[string]*wire.ChatItem{}
	p.req.At = time.Now()
	c.pending = []*pendingReq{p}
	h.setState(c, wire.AgentWaiting, waitingFor(p))
	h.mu.Unlock()
	h.notify()
}

// settle frees a chat whose turn-end question was answered, in the mode
// the answer chose ("" keeps it).
func (h *chatHost) settle(c *chatState, mode string) {
	h.mu.Lock()
	if mode != "" {
		c.rec.Mode = mode
	}
	h.setState(c, wire.AgentReady, "")
	h.mu.Unlock()
	h.notify()
}

// waitingFor says what a chat waits on: permission, question or plan.
func waitingFor(p *pendingReq) string {
	if p.req.Kind == "tool" {
		return "permission"
	}
	return p.req.Kind
}

// cancel drops a request the agent no longer waits on.
func (h *chatHost) cancel(c *chatState, id string) {
	h.mu.Lock()
	for i, p := range c.pending {
		if p.req.ID == id {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			break
		}
	}
	if len(c.pending) == 0 && c.state == wire.AgentWaiting {
		h.setState(c, wire.AgentWorking, "")
	}
	h.mu.Unlock()
	h.notify()
}

// decide answers a request.
func (h *chatHost) decide(chatID, reqID string, d decision) error {
	switch d.Behavior {
	case "allow", "allowSession", "deny":
	default:
		return errors.New("a decision is allow, allowSession or deny")
	}
	h.mu.Lock()
	c := h.chats[chatID]
	var p *pendingReq
	if c != nil {
		for i, q := range c.pending {
			if q.req.ID == reqID {
				p = q
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				break
			}
		}
	}
	if p == nil {
		h.mu.Unlock()
		return errors.New("that request is no longer waiting")
	}
	if len(c.pending) == 0 {
		h.setState(c, wire.AgentWorking, "")
	} else {
		h.setState(c, wire.AgentWaiting, waitingFor(c.pending[0]))
	}
	h.mu.Unlock()
	err := p.answer(d)
	if err != nil {
		h.mu.Lock()
		c.pending = append([]*pendingReq{p}, c.pending...)
		h.setState(c, wire.AgentWaiting, waitingFor(p))
		h.mu.Unlock()
	}
	h.notify()
	return err
}

// interrupt stops a chat's turn.
func (h *chatHost) interrupt(chatID string) error {
	h.mu.Lock()
	c := h.chats[chatID]
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("no chat %s", chatID)
	}
	rt := c.rt
	busy := c.state == wire.AgentWorking || c.state == wire.AgentWaiting
	h.mu.Unlock()
	if !busy || rt == nil || !rt.alive() {
		return nil
	}
	return rt.interrupt()
}

type configureArgs struct {
	Chat   string `json:"chat"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Mode   string `json:"mode"`
	Title  string `json:"title"`
}

// configure changes a chat's model, effort, mode or title.
func (h *chatHost) configure(a configureArgs) error {
	h.mu.Lock()
	c := h.chats[a.Chat]
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("no chat %s", a.Chat)
	}
	if err := checkMode(c.rec.Agent, a.Mode); err != nil {
		h.mu.Unlock()
		return err
	}
	if a.Model != "" {
		c.rec.Model = a.Model
	}
	if a.Effort != "" {
		c.rec.Effort = a.Effort
	}
	if a.Mode != "" {
		c.rec.Mode = a.Mode
	}
	if a.Title != "" {
		c.rec.Title = titleOf(a.Title)
	}
	h.save(c)
	rec, rt := c.rec, c.rt
	h.mu.Unlock()
	h.notify()
	if rt != nil && rt.alive() && (a.Model != "" || a.Effort != "" || a.Mode != "") {
		return rt.configure(rec)
	}
	return nil
}

// checkMode refuses a permission mode the agent doesn't have.
func checkMode(agent, mode string) error {
	if mode == "" {
		return nil
	}
	ok := map[string][]string{
		"claude": {"default", "acceptEdits", "plan", "bypassPermissions"},
		"codex":  {"readOnly", "default", "plan", "bypassPermissions"},
	}[agent]
	for _, m := range ok {
		if m == mode {
			return nil
		}
	}
	return fmt.Errorf("%s has no permission mode %q", agent, mode)
}

// closeChat stops a chat and puts it away (~/.pier/chats-closed).
func (h *chatHost) closeChat(chatID string) error {
	h.mu.Lock()
	c := h.chats[chatID]
	if c == nil {
		h.mu.Unlock()
		return nil
	}
	delete(h.chats, chatID)
	rt := c.rt
	c.rt = nil
	h.mu.Unlock()
	if rt != nil {
		rt.stop()
	}
	c.log.close()
	dst := filepath.Join(h.home, ".pier", "chats-closed")
	_ = os.MkdirAll(dst, 0o700)
	err := os.Rename(c.dir, filepath.Join(dst, chatID))
	h.notify()
	return err
}

// dequeue removes a prompt still waiting in a chat's queue.
func (h *chatHost) dequeue(chatID, promptID string) error {
	h.mu.Lock()
	c := h.chats[chatID]
	h.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no chat %s", chatID)
	}
	if !dropQueued(c.dir, promptID) {
		return errors.New("that prompt is no longer queued")
	}
	h.notify()
	return nil
}

// reap stops programs idle past idleStop: their conversations resume on
// the next prompt.
func (h *chatHost) reap() {
	h.mu.Lock()
	var stop []agentProc
	for _, c := range h.chats {
		if c.rt != nil && c.state == wire.AgentReady && !c.starting && time.Since(c.activity) > idleStop {
			stop = append(stop, c.rt)
			c.rt = nil
		}
	}
	h.mu.Unlock()
	for _, rt := range stop {
		rt.stop()
	}
	if h.codex != nil {
		h.codex.reapIfIdle()
	}
}

// turnOpen reports a turn running anywhere: the session is busy.
func (h *chatHost) turnOpen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.chats {
		if c.state == wire.AgentWorking && time.Since(c.activity) < 20*time.Minute {
			return true
		}
	}
	return false
}

// busy reports a chat mid-turn or waiting: replacing the supervisor now
// would cut it off.
func (h *chatHost) busy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.chats {
		if c.state == wire.AgentWorking || c.state == wire.AgentWaiting || c.state == wire.AgentTerminal {
			return true
		}
	}
	return false
}

// --- what frontends see ----------------------------------------------------

func (h *chatHost) info(c *chatState) wire.ChatInfo {
	in := wire.ChatInfo{
		ChatSummary: h.summary(c),
		Model:       c.rec.Model, Effort: c.rec.Effort, Mode: c.rec.Mode,
		Created: c.rec.Created, Error: c.rec.Error, Queue: []wire.QueuedPrompt{},
	}
	if len(c.pending) > 0 {
		r := c.pending[0].req
		in.Pending = &r
	}
	for _, p := range queued(c.dir) {
		in.Queue = append(in.Queue, wire.QueuedPrompt{ID: p.ID, Text: truncate(p.Text, 2000), Attachments: p.Attachments, At: p.At})
	}
	return in
}

func (h *chatHost) summary(c *chatState) wire.ChatSummary {
	s := wire.ChatSummary{ID: c.rec.ID, Agent: c.rec.Agent, Title: c.rec.Title, State: c.state, Waiting: c.waiting, Since: c.since, Last: truncate(c.rec.Last, 200)}
	if s.Title == "" {
		s.Title = "New chat"
	}
	if len(c.pending) > 0 {
		s.Ask = c.pending[0].req.Summary
	}
	s.Queued = len(queued(c.dir))
	return s
}

func (h *chatHost) sorted() []*chatState {
	out := make([]*chatState, 0, len(h.chats))
	for _, c := range h.chats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rec.Created.Before(out[j].rec.Created) })
	return out
}

// topic is the "chats" stream topic.
func (h *chatHost) topic() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	infos := []wire.ChatInfo{}
	for _, c := range h.sorted() {
		infos = append(infos, h.info(c))
	}
	return map[string]any{"chats": infos}
}

// summaries are the chats in the status beacon.
func (h *chatHost) summaries() []wire.ChatSummary {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []wire.ChatSummary
	for _, c := range h.sorted() {
		out = append(out, h.summary(c))
	}
	return out
}

// streamingItems are a chat's items being written, for a frontend that
// starts following it.
func (h *chatHost) streamingItems(chatID string) []wire.ChatItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.chats[chatID]
	if c == nil {
		return nil
	}
	out := []wire.ChatItem{}
	for _, it := range c.streaming {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func (h *chatHost) logOf(chatID string) *chatLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.chats[chatID]; c != nil {
		return c.log
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// --- ops -------------------------------------------------------------------

func registerChatOps(h *chatHost, touch func()) {
	ops["chat.create"] = func(raw json.RawMessage) (any, error) {
		var a createArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		touch()
		return nil, h.create(a)
	}
	ops["chat.send"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Chat   string `json:"chat"`
			Mode   string `json:"mode"`
			Prompt prompt `json:"prompt"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		touch()
		return nil, h.send(a.Chat, a.Prompt, a.Mode)
	}
	ops["chat.dequeue"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Chat, Prompt string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return nil, h.dequeue(a.Chat, a.Prompt)
	}
	ops["chat.interrupt"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Chat string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		touch()
		return nil, h.interrupt(a.Chat)
	}
	ops["chat.decide"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Chat, ID, Behavior, Message, Mode string
			Answers, Notes                    map[string]string
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		touch()
		return nil, h.decide(a.Chat, a.ID, decision{Behavior: a.Behavior, Message: a.Message, Answers: a.Answers, Notes: a.Notes, Mode: a.Mode})
	}
	ops["chat.configure"] = func(raw json.RawMessage) (any, error) {
		var a configureArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return nil, h.configure(a)
	}
	ops["chat.close"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Chat string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return nil, h.closeChat(a.Chat)
	}
	ops["chat.page"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Chat   string `json:"chat"`
			Before string `json:"before"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		l := h.logOf(a.Chat)
		if l == nil {
			return nil, fmt.Errorf("no chat %s", a.Chat)
		}
		items, _, err := readItems(l.path())
		if err != nil {
			return nil, err
		}
		end := len(items)
		for i, it := range items {
			if it.ID == a.Before {
				end = i
				break
			}
		}
		if a.Limit <= 0 || a.Limit > 500 {
			a.Limit = 300
		}
		from := max(end-a.Limit, 0)
		return map[string]any{"items": items[from:end], "more": from > 0}, nil
	}
	ops["models"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Agent string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return h.models(a.Agent)
	}
	ops["touch"] = func(json.RawMessage) (any, error) {
		touch()
		return nil, nil
	}
}

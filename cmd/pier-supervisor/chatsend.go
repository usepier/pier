package main

// Starting chats and getting prompts to them: a prompt waits in the
// chat's queue (a file each, so a restart loses none) until its agent is
// free — or goes into the running turn, steering it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

type createArgs struct {
	ID     string  `json:"id"`
	Agent  string  `json:"agent"`
	Model  string  `json:"model"`
	Effort string  `json:"effort"`
	Fast   bool    `json:"fast"`
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
	rec := chatRecord{ID: a.ID, Agent: a.Agent, Title: a.Title, Model: a.Model, Effort: a.Effort, Fast: a.Fast, Mode: a.Mode, Created: time.Now()}
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
	dir, agent := c.dir, c.rec.Agent
	h.mu.Unlock()
	if !free {
		return
	}
	q := queued(dir)
	if len(q) == 0 {
		return
	}
	p := q[0]
	// Naming the branch needs only the prompt: it starts while setup runs.
	if p.NameBranch != "" {
		h.nameBranch(agent, p.NameBranch, p.Text)
	}
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

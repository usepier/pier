package main

// What the agents report, and what frontends answer them with: items,
// requests waiting on the user, decisions, interrupts, changes of model or
// mode, and stopping agents nobody uses.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

// apply records what an agent's stream says. It runs on the program's
// reader goroutine.
func (h *chatHost) apply(c *chatState, ev chat.Event) {
	var items []wire.ChatItem
	for _, ch := range ev.Changes {
		items = append(items, ch.Item)
	}
	c.log.put(items...)
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
	Fast   string `json:"fast"` // on | off | "" (as it is)
	Mode   string `json:"mode"`
	Title  string `json:"title"`
}

// configure changes a chat's model, effort, fast mode, permission mode or
// title.
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
	if a.Fast != "" {
		c.rec.Fast = a.Fast == "on"
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
	if rt != nil && rt.alive() && (a.Model != "" || a.Effort != "" || a.Fast != "" || a.Mode != "") {
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

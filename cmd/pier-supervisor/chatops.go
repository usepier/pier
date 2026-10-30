package main

// What frontends see of the chats, and the ops they act on them with.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

func (h *chatHost) info(c *chatState) wire.ChatInfo {
	in := wire.ChatInfo{
		ChatSummary: h.summary(c),
		Model:       c.rec.Model, Effort: c.rec.Effort, Fast: c.rec.Fast, FastOff: c.fastOff, Mode: c.rec.Mode,
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
	queries["chat.page"] = func(raw json.RawMessage) (any, error) {
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
	queries["models"] = func(raw json.RawMessage) (any, error) {
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

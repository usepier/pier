package main

// Continuing a chat in a terminal: the headless program steps aside and
// the agent's own interface takes the conversation over (claude --resume,
// codex resume) in a tmux window. The chat waits meanwhile — prompts sent
// to it queue — and when the terminal program exits, what happened there
// comes back into the chat, read from the agent's own record of the
// conversation, and the chat goes on headless.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/chatfile"
	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

// takeoverCommand is the program that continues a chat in a terminal.
type takeoverCommand struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

// transcriptOf finds the agent's own record of a conversation.
func transcriptOf(home string, rec chatRecord) string {
	var pattern string
	switch rec.Agent {
	case "codex":
		if rec.Conversation == "" {
			return ""
		}
		pattern = filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+filepath.Base(rec.Conversation)+".jsonl")
	default:
		pattern = filepath.Join(home, ".claude", "projects", "*", filepath.Base(rec.Conversation)+".jsonl")
	}
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	return matches[0]
}

// takeoverArgv is the command line that continues rec in a terminal.
func takeoverArgv(bin string, rec chatRecord) []string {
	switch rec.Agent {
	case "codex":
		args := []string{bin, "resume", rec.Conversation}
		if rec.Model != "" {
			args = append(args, "-m", rec.Model)
		}
		if rec.Effort != "" {
			args = append(args, "-c", "model_reasoning_effort="+rec.Effort)
		}
		switch rec.Mode {
		case "readOnly":
			args = append(args, "-s", "read-only", "-a", "on-request")
		case "bypassPermissions":
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		default:
			args = append(args, "-s", "workspace-write", "-a", "on-request")
		}
		return args
	}
	args := []string{bin}
	if rec.Started {
		args = append(args, "--resume", rec.Conversation)
	} else {
		args = append(args, "--session-id", rec.Conversation)
	}
	if rec.Model != "" {
		args = append(args, "--model", rec.Model)
	}
	if rec.Mode != "" {
		args = append(args, "--permission-mode", rec.Mode)
	}
	if rec.Effort != "" {
		args = append(args, "--effort", rec.Effort)
	}
	return args
}

// beginTakeover hands a chat to a terminal program (pid is the process
// that will run it): the headless program stops, the chat waits.
func (h *chatHost) beginTakeover(chatID string, pid int) (takeoverCommand, error) {
	h.mu.Lock()
	c := h.chats[chatID]
	if c == nil {
		h.mu.Unlock()
		return takeoverCommand{}, fmt.Errorf("no chat %s", chatID)
	}
	switch c.state {
	case wire.AgentWorking, wire.AgentWaiting:
		h.mu.Unlock()
		return takeoverCommand{}, errors.New("the agent is mid-turn: let it finish, or stop it, first")
	case wire.AgentTerminal:
		if c.rec.Takeover != nil && alive(c.rec.Takeover.PID) {
			h.mu.Unlock()
			return takeoverCommand{}, errors.New("this chat already goes on in a terminal")
		}
	}
	if c.rec.Agent == "codex" && c.rec.Conversation == "" {
		h.mu.Unlock()
		return takeoverCommand{}, errors.New("this chat has no conversation yet: send it a first prompt")
	}
	rt := c.rt
	c.rt = nil
	rec := c.rec
	h.mu.Unlock()
	if rt != nil {
		rt.stop()
	}
	bin, note := h.program(rec.Agent)
	if bin == "" {
		return takeoverCommand{}, errors.New(note)
	}
	t := &chatfile.Takeover{PID: pid, Started: time.Now()}
	if path := transcriptOf(h.home, rec); path != "" {
		t.Transcript = path
		if fi, err := os.Stat(path); err == nil {
			t.Offset = fi.Size()
		}
	}
	h.mu.Lock()
	c.rec.Takeover = t
	h.setState(c, wire.AgentTerminal, "")
	h.mu.Unlock()
	c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: "continued in a terminal"})
	h.notify()
	return takeoverCommand{Argv: takeoverArgv(bin, rec), Dir: checkoutDir(h.home), Env: agentEnv(h.home)}, nil
}

// endTakeover brings what happened in the terminal back into the chat.
func (h *chatHost) endTakeover(chatID string) error {
	h.mu.Lock()
	c := h.chats[chatID]
	if c == nil {
		h.mu.Unlock()
		return fmt.Errorf("no chat %s", chatID)
	}
	t := c.rec.Takeover
	rec := c.rec
	h.mu.Unlock()
	if t == nil {
		return nil
	}
	path := t.Transcript
	offset := t.Offset
	if path == "" {
		path, offset = transcriptOf(h.home, rec), 0
	}
	var items []wire.ChatItem
	if path != "" {
		var err error
		items, err = importTranscript(rec.Agent, path, offset, "t"+newOpID()+"-")
		if err != nil {
			fmt.Println("pier-supervisor: reading back chat", chatID+":", err)
		}
	}
	text := "back from the terminal"
	if len(items) == 0 {
		text = "back from the terminal (nothing new there)"
	}
	items = append(items, wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: text})
	c.log.put(items...)
	h.mu.Lock()
	c.rec.Takeover = nil
	if path != "" {
		c.rec.Started = true
	}
	for _, it := range items {
		if it.Kind == wire.ChatAssistant && !it.Sidechain {
			c.rec.Last = truncate(it.Text, 400)
		}
	}
	h.setState(c, wire.AgentReady, "")
	h.mu.Unlock()
	h.notify()
	go h.deliver(c)
	return nil
}

// importTranscript reads what an agent's record of a conversation gained
// past offset, as chat items.
func importTranscript(agent, path string, offset int64, prefix string) ([]wire.ChatItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	p := chat.NewParser(agent, prefix)
	var items []wire.ChatItem
	index := map[string]int{}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 && line[len(line)-1] == '\n' {
			for _, ch := range p.Feed(line) {
				if i, ok := index[ch.Item.ID]; ok {
					items[i] = ch.Item
					continue
				}
				index[ch.Item.ID] = len(items)
				items = append(items, ch.Item)
			}
		}
		if err == io.EOF {
			return items, nil
		}
		if err != nil {
			return items, err
		}
	}
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// reapTakeovers ends take-overs whose terminal program is gone without
// saying so (its window killed, the session restarted).
func (h *chatHost) reapTakeovers() {
	h.mu.Lock()
	var ended []string
	for id, c := range h.chats {
		if t := c.rec.Takeover; t != nil && !alive(t.PID) {
			ended = append(ended, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ended {
		h.endTakeover(id)
	}
}

func registerTakeoverOps(h *chatHost) {
	ops["chat.takeover.begin"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Chat string `json:"chat"`
			PID  int    `json:"pid"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return h.beginTakeover(a.Chat, a.PID)
	}
	ops["chat.takeover.end"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Chat string `json:"chat"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return nil, h.endTakeover(a.Chat)
	}
}

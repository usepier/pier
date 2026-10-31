package api

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// notices tells the client what deserves the user's attention (notify): a
// chat that finished its turn or needs them, a setup or create that
// failed. Polls of the overview find them from the sessions' beacons — no
// stream is held open for that; a chat the client is watching finds them
// sooner.
type notices struct {
	s *Server

	mu     sync.Mutex
	primed bool              // the first poll only records: no notices for the past
	sent   map[string]bool   // notice ids already sent
	chats  map[string]string // session/chat → state last seen
	last   map[string]pier.Session
}

func newNotices(s *Server) *notices {
	return &notices{s: s, sent: map[string]bool{}, chats: map[string]string{}, last: map[string]pier.Session{}}
}

func (n *notices) send(sess pier.Session, chatID, kind, key, title, body string) {
	id := sess.ID + ":" + chatID + ":" + kind + ":" + key
	n.mu.Lock()
	if n.sent[id] {
		n.mu.Unlock()
		return
	}
	n.sent[id] = true
	n.mu.Unlock()
	n.s.notify("notify", wire.Notice{ID: id, Kind: kind, SessionID: sess.ID, ChatID: chatID, Title: title, Body: body})
}

// chat notes a chat's state, from a stream or a poll.
func (n *notices) chat(sess pier.Session, c wire.ChatInfo) {
	if sess.PoolGen != "" {
		return
	}
	k := sess.ID + "/" + c.ID
	n.mu.Lock()
	prev, seen := n.chats[k]
	n.chats[k] = string(c.State)
	primed := n.primed
	n.mu.Unlock()
	if !primed || !seen || prev == string(c.State) {
		return
	}
	key := strconv.FormatInt(c.Since.Unix(), 10)
	title := sess.Name
	if c.Title != "" {
		title += " · " + c.Title
	}
	switch {
	case c.State == wire.AgentWaiting:
		n.send(sess, c.ID, "needs_input", key, title+" needs you", askBody(c))
	case c.State == wire.AgentReady && (prev == string(wire.AgentWorking) || prev == string(wire.AgentWaiting)):
		body := c.Last
		if body == "" {
			body = "Its turn is done."
		}
		n.send(sess, c.ID, "finished", key, title+" finished", body)
	case c.State == wire.AgentError:
		n.send(sess, c.ID, "chat_failed", key, title+" stopped", c.Error)
	}
}

// askBody says what a waiting chat asks.
func askBody(c wire.ChatInfo) string {
	p := c.Pending
	switch {
	case p == nil && c.Ask != "":
		return c.Ask
	case p == nil:
		return "It's waiting for your answer."
	case len(p.Questions) > 0:
		body := p.Questions[0].Question
		if len(p.Questions) > 1 {
			body += fmt.Sprintf(" (and %d more)", len(p.Questions)-1)
		}
		return body
	case p.Kind == "plan":
		return "Its plan is ready for you to review."
	}
	return fmt.Sprintf("Allow %s: %s?", p.Tool, p.Summary)
}

// pr notes a session's pull request: checks that just failed are news.
func (n *notices) pr(sess pier.Session, old, cur pier.PR) {
	if cur.Number == 0 || (cur.State != "open" && cur.State != "draft") || cur.ChecksState() != "failing" {
		return
	}
	if old.Number == cur.Number && old.HeadOid == cur.HeadOid && old.ChecksState() == "failing" {
		return
	}
	n.mu.Lock()
	primed := n.primed
	n.mu.Unlock()
	if !primed {
		return
	}
	var failing []string
	for _, c := range cur.Checks {
		if c.State == "failing" {
			failing = append(failing, c.Name)
		}
	}
	body := fmt.Sprintf("PR #%d: %s", cur.Number, strings.Join(failing, ", "))
	n.send(sess, "", "ci_failed", cur.HeadOid, sess.Name+"'s checks failed", body)
}

// poll notes what an enriched listing says.
func (n *notices) poll(sessions []pier.Session) {
	n.mu.Lock()
	primed := n.primed
	n.primed = true
	last := n.last
	n.last = map[string]pier.Session{}
	for _, s := range sessions {
		n.last[s.ID] = s
	}
	n.mu.Unlock()
	for _, s := range sessions {
		if s.PoolGen != "" {
			continue
		}
		prev, had := last[s.ID]
		if primed && had {
			if prev.Setup == "running" && s.Setup == "failed" {
				n.send(s, "", "setup_failed", "", s.Name+"'s setup failed", "The repo's setup script exited with an error.")
			}
			if prev.State == pier.StateCreating && s.State == pier.StateFailed {
				n.send(s, "", "create_failed", "", s.Name+" couldn't be created", s.FailReason)
			}
		}
		for _, c := range s.Chats {
			n.chat(s, wire.ChatInfo{ChatSummary: c})
		}
	}
}

func init() {
	serverNote[wire.Notice]("notify", "Something about a session deserves the user's attention: a chat finished or needs them, its setup or create failed. Each id is sent once.")
}

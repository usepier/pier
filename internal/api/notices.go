package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// notices tells the client what deserves the user's attention (notify): an
// agent that finished its turn or needs them, a setup or create that
// failed. Polls of the overview find them; the agents' streams find them
// sooner.
//
// While the client answers permission requests, a keeper follows every
// session whose agent is at work, so a request reaches the client (and its
// notification) even when that session's chat isn't open.
type notices struct {
	s *Server

	mu      sync.Mutex
	primed  bool              // the first poll only records: no notices for the past
	sent    map[string]bool   // notice ids already sent
	agents  map[string]string // session id → agent state last seen
	last    map[string]pier.Session
	keepers map[string]*keeper
}

type keeper struct {
	cancel context.CancelFunc
	idle   time.Time // when its agent stopped needing one; zero while it does
}

// keeperGrace keeps a keeper a while after its agent's turn ends: the next
// prompt usually follows soon.
const keeperGrace = 2 * time.Minute

func newNotices(s *Server) *notices {
	return &notices{s: s, sent: map[string]bool{}, agents: map[string]string{}, last: map[string]pier.Session{}, keepers: map[string]*keeper{}}
}

func (n *notices) send(sess pier.Session, kind, key, title, body string) {
	id := sess.ID + ":" + kind + ":" + key
	n.mu.Lock()
	if n.sent[id] {
		n.mu.Unlock()
		return
	}
	n.sent[id] = true
	n.mu.Unlock()
	n.s.notify("notify", wire.Notice{ID: id, Kind: kind, SessionID: sess.ID, Title: title, Body: body})
}

// agent notes a session's agent state, from a stream or a poll.
func (n *notices) agent(sess pier.Session, a *wire.AgentDetail) {
	if a == nil || sess.PoolGen != "" {
		return
	}
	n.mu.Lock()
	prev, seen := n.agents[sess.ID]
	n.agents[sess.ID] = string(a.State)
	primed := n.primed
	n.mu.Unlock()
	if !primed || !seen || prev == string(a.State) {
		return
	}
	key := strconv.FormatInt(a.Since.Unix(), 10)
	switch {
	case a.State == wire.AgentWaiting:
		body := "It's waiting for your answer."
		if p := a.Pending; p != nil {
			body = fmt.Sprintf("Allow %s: %s?", p.Tool, p.Summary)
		}
		n.send(sess, "needs_input", key, sess.Name+" needs you", body)
	case a.State == wire.AgentReady && (prev == string(wire.AgentWorking) || prev == string(wire.AgentWaiting)):
		body := a.Last
		if body == "" {
			body = "Its turn is done."
		}
		n.send(sess, "finished", key, sess.Name+" finished", body)
	}
}

// poll notes what an enriched listing says and keeps the keepers in step.
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
				n.send(s, "setup_failed", "", s.Name+"'s setup failed", "The repo's setup script exited with an error.")
			}
			if prev.State == pier.StateCreating && s.State == pier.StateFailed {
				n.send(s, "create_failed", "", s.Name+" couldn't be created", s.FailReason)
			}
		}
		if s.Agent != nil {
			n.agent(s, agentFromStatus(s.Agent))
		}
	}
	n.keep(sessions)
}

// keep starts a keeper for each session whose agent is at work, and stops
// those whose agent hasn't been for a while.
func (n *notices) keep(sessions []pier.Session) {
	if !n.s.approvals() {
		n.stop()
		return
	}
	be, err := n.s.backend()
	if err != nil {
		return
	}
	now := time.Now()
	wanted := map[string]pier.Session{}
	for _, s := range sessions {
		a := s.Agent
		if a != nil && s.PoolGen == "" && running(s) && be.HasKey(s) &&
			(a.State == "working" || a.State == "waiting" || a.State == "starting") {
			wanted[s.ID] = s
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, k := range n.keepers {
		if _, ok := wanted[id]; ok {
			k.idle = time.Time{}
			continue
		}
		if k.idle.IsZero() {
			k.idle = now
		}
		if now.Sub(k.idle) > keeperGrace {
			k.cancel()
			delete(n.keepers, id)
		}
	}
	for id, s := range wanted {
		if n.keepers[id] != nil {
			continue
		}
		ctx, cancel := context.WithCancel(n.s.root)
		n.keepers[id] = &keeper{cancel: cancel}
		go n.follow(ctx, be, s)
	}
}

// follow is one keeper: the session's agent stream, reconnecting gently,
// until stopped.
func (n *notices) follow(ctx context.Context, be Backend, sess pier.Session) {
	for attempt := 1; ctx.Err() == nil; attempt++ {
		started := time.Now()
		err := be.Follow(ctx, sess, []string{"agent", "approvals"}, func(ev string, data json.RawMessage) {
			if ev != "agent" {
				return
			}
			if d, err := pier.ParseAgent(data); err == nil {
				n.agent(sess, wireAgentDetail(d))
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			n.s.logf("following %s's agent: %v", sess.Name, err)
		}
		if time.Since(started) > time.Minute {
			attempt = 1
		}
		if !sleepCtx(ctx, time.Duration(min(attempt, 6))*5*time.Second) {
			return
		}
	}
}

func (n *notices) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, k := range n.keepers {
		k.cancel()
		delete(n.keepers, id)
	}
}

func init() {
	serverNote[wire.Notice]("notify", "Something about a session deserves the user's attention: its agent finished or needs them, its setup or create failed. Each id is sent once.")
}

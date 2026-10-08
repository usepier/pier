package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

// A chat is a session's agent conversation, read from the transcript its
// CLI writes. This machine keeps a copy (pier.Mirror), so a parked session
// still shows its chat and a reconnect fetches only what is new. One
// follower per session keeps the copy current while anyone watches.

const (
	chatPage = 300 // items a snapshot or a history page carries
	// chatReadyWait bounds how long a watch waits for the session to catch
	// its copy up before answering with what it has.
	chatReadyWait = 20 * time.Second
)

// chatFeed is a session's transcript copy, parsed into items.
type chatFeed struct {
	mirror *pier.Mirror
	parser chat.Parser
	items  []wire.ChatItem
	index  map[string]int
}

func openChatFeed(id string) (*chatFeed, error) {
	m, err := pier.OpenMirror(id)
	if err != nil {
		return nil, err
	}
	f := &chatFeed{mirror: m}
	f.restart(m.Meta().Kind)
	lines, err := m.Lines()
	if err != nil {
		m.Close()
		return nil, err
	}
	for _, l := range lines {
		f.fold(f.parser.Feed(l))
	}
	return f, nil
}

func (f *chatFeed) close() { f.mirror.Close() }

func (f *chatFeed) restart(kind string) {
	f.parser, f.items, f.index = chat.NewParser(kind), nil, map[string]int{}
}

func (f *chatFeed) partial() bool { return f.parser.Partial() || f.mirror.Meta().Skipped > 0 }

// fold applies parser changes and returns the items they added and the
// earlier ones they changed, each with its latest content.
func (f *chatFeed) fold(cs []chat.Change) (added, updated []wire.ChatItem) {
	start := len(f.items)
	changed := map[string]bool{}
	var order []string
	for _, c := range cs {
		if i, ok := f.index[c.Item.ID]; ok {
			f.items[i] = c.Item
			if i < start && !changed[c.Item.ID] {
				changed[c.Item.ID] = true
				order = append(order, c.Item.ID)
			}
			continue
		}
		f.index[c.Item.ID] = len(f.items)
		f.items = append(f.items, c.Item)
	}
	for _, id := range order {
		updated = append(updated, f.items[f.index[id]])
	}
	return f.items[start:], updated
}

// apply adds a chunk of the transcript and returns what the chat's
// watchers need to hear.
func (f *chatFeed) apply(ch pier.TranscriptChunk) ([]wire.ChatChange, error) {
	reset := ch.Reset || ch.File != f.mirror.Meta().File
	if err := f.mirror.Apply(ch); err != nil {
		return nil, err
	}
	if reset {
		f.restart(ch.Kind)
	}
	var cs []chat.Change
	for _, line := range bytes.Split([]byte(ch.Data), []byte{'\n'}) {
		if len(line) > 0 {
			cs = append(cs, f.parser.Feed(line)...)
		}
	}
	added, updated := f.fold(cs)
	if reset {
		items, more := f.tail()
		return []wire.ChatChange{{Op: wire.ChatReset, Items: items, More: more, Partial: f.partial()}}, nil
	}
	var out []wire.ChatChange
	if len(updated) > 0 {
		out = append(out, wire.ChatChange{Op: wire.ChatUpdate, Items: updated, Partial: f.partial()})
	}
	if len(added) > 0 {
		out = append(out, wire.ChatChange{Op: wire.ChatAppend, Items: append([]wire.ChatItem(nil), added...), Partial: f.partial()})
	}
	return out, nil
}

// tail is the newest page of items.
func (f *chatFeed) tail() ([]wire.ChatItem, bool) {
	from := max(len(f.items)-chatPage, 0)
	return append([]wire.ChatItem{}, f.items[from:]...), from > 0
}

// before pages older items in: up to limit items before the one with id.
func (f *chatFeed) before(id string, limit int) wire.HistoryPage {
	if limit <= 0 || limit > chatPage {
		limit = chatPage
	}
	end, ok := f.index[id]
	if !ok {
		return wire.HistoryPage{Items: []wire.ChatItem{}}
	}
	from := max(end-limit, 0)
	return wire.HistoryPage{Items: append([]wire.ChatItem{}, f.items[from:end]...), More: from > 0}
}

// chatSession follows one session's chat for its watchers.
type chatSession struct {
	s  *Server
	id string

	mu        sync.Mutex
	feed      *chatFeed
	agent     *wire.AgentDetail
	live      bool
	gap       bool // the copy fell out of step: follow again from scratch
	listeners map[int]func(wire.ChatChange)
	nextL     int
	refs      int
	cancel    context.CancelFunc

	ready     chan struct{} // closed once the first catch-up is done (or failed)
	readyOnce sync.Once
}

// chat returns the session's chat follower, starting it if needed, and a
// release for when the caller is done with it.
func (s *Server) chat(id string) (*chatSession, func(), error) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	cs := s.chats[id]
	if cs == nil {
		feed, err := openChatFeed(id)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithCancel(s.root)
		cs = &chatSession{s: s, id: id, feed: feed, listeners: map[int]func(wire.ChatChange){}, cancel: cancel, ready: make(chan struct{})}
		s.chats[id] = cs
		go cs.run(ctx)
	}
	cs.refs++
	var once sync.Once
	return cs, func() {
		once.Do(func() {
			s.chatMu.Lock()
			cs.refs--
			last := cs.refs == 0
			if last {
				delete(s.chats, id)
			}
			s.chatMu.Unlock()
			if last {
				cs.cancel()
			}
		})
	}, nil
}

func (cs *chatSession) markReady() { cs.readyOnce.Do(func() { close(cs.ready) }) }

// emit sends a change to every watcher. Called with mu held, so a watcher
// sees changes in order and never one its snapshot already holds.
func (cs *chatSession) emit(c wire.ChatChange) {
	for _, fn := range cs.listeners {
		fn(c)
	}
}

// subscribe takes a snapshot and adds a watcher in one step.
func (cs *chatSession) subscribe(fn func(wire.ChatChange)) (wire.ChatSnapshot, func()) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	items, more := cs.feed.tail()
	snap := wire.ChatSnapshot{Items: items, More: more, Agent: cs.agent, Live: cs.live, Partial: cs.feed.partial()}
	if !cs.live {
		snap.AsOf = cs.asOf()
	}
	cs.nextL++
	id := cs.nextL
	cs.listeners[id] = fn
	return snap, func() {
		cs.mu.Lock()
		delete(cs.listeners, id)
		cs.mu.Unlock()
	}
}

func (cs *chatSession) asOf() *time.Time {
	if t := cs.feed.mirror.Meta().Synced; !t.IsZero() {
		return &t
	}
	return nil
}

func (cs *chatSession) setLive(live bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.live == live {
		return
	}
	cs.live = live
	c := wire.ChatChange{Op: wire.ChatAgent, Agent: cs.agent, Live: &live}
	if !live {
		c.AsOf = cs.asOf()
	}
	cs.emit(c)
}

func (cs *chatSession) setAgent(a *wire.AgentDetail) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if reflect.DeepEqual(cs.agent, a) {
		return
	}
	cs.agent = a
	cs.emit(wire.ChatChange{Op: wire.ChatAgent, Agent: a})
}

// run keeps the copy current while the session runs, and waits for it to
// run while it doesn't.
func (cs *chatSession) run(ctx context.Context) {
	defer func() {
		cs.mu.Lock()
		cs.feed.close()
		cs.mu.Unlock()
	}()
	for attempt := 0; ctx.Err() == nil; {
		be, sess, err := cs.s.sessionFor(ctx, cs.id)
		if err != nil || !running(sess) || !be.HasKey(sess) {
			if err == nil && !cs.isLive() {
				cs.setAgent(agentFromStatus(sess.Agent))
			}
			cs.setLive(false)
			cs.markReady()
			if !sleepCtx(ctx, chatRecheck) {
				return
			}
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		cs.mu.Lock()
		meta := cs.feed.mirror.Meta()
		cs.mu.Unlock()
		started := time.Now()
		err = be.FollowTranscript(fctx, sess, cs.s.agentTopics(), meta.File, meta.Next, func(ev string, data json.RawMessage) {
			cs.onEvent(sess, ev, data, cancel)
		})
		cancel()
		cs.setLive(false)
		cs.markReady() // a first connect that failed still lets watchers see the copy
		cs.mu.Lock()
		gap := cs.gap
		cs.gap = false
		cs.mu.Unlock()
		if gap {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			cs.s.logf("chat %s: %v", sess.Name, err)
		}
		if time.Since(started) > time.Minute {
			attempt = 0
		}
		attempt++
		if !sleepCtx(ctx, time.Duration(min(attempt, 6))*5*time.Second) {
			return
		}
	}
}

// running reports a session whose VM runs: one to follow.
func running(s pier.Session) bool {
	switch s.State {
	case pier.StateRunning, pier.StateWorking, pier.StateIdle:
		return true
	}
	return false
}

// chatRecheck is how often a chat whose session isn't running looks again.
var chatRecheck = 5 * time.Second

func (cs *chatSession) isLive() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.live
}

func (cs *chatSession) onEvent(sess pier.Session, ev string, data json.RawMessage, stop func()) {
	switch ev {
	case "tx":
		var ch pier.TranscriptChunk
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		cs.setLive(true)
		cs.mu.Lock()
		changes, err := cs.feed.apply(ch)
		if err != nil {
			// Start the copy over: the next follow sends it all.
			cs.s.logf("chat %s: %v — reading the transcript again", sess.Name, err)
			cs.feed.mirror.Reset()
			cs.feed.restart("")
			cs.gap = true
			cs.mu.Unlock()
			stop()
			return
		}
		for _, c := range changes {
			cs.emit(c)
		}
		cs.mu.Unlock()
		if ch.EOF {
			cs.markReady()
		}
	case "agent":
		d, err := pier.ParseAgent(data)
		if err != nil {
			return
		}
		cs.setLive(true)
		w := wireAgentDetail(d)
		cs.setAgent(w)
		cs.s.notices.agent(sess, w)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func wireAgentDetail(d *pier.AgentDetail) *wire.AgentDetail {
	if d == nil {
		return nil
	}
	w := &wire.AgentDetail{
		AgentStatus: wire.AgentStatus{Kind: d.Kind, State: wire.AgentState(d.State), Waiting: d.Waiting, Since: d.Since.UTC(), Last: d.Last, Queued: d.Queued},
		Model:       d.Model,
		Queue:       []wire.QueuedPrompt{},
	}
	if p := d.Pending; p != nil {
		w.Pending = &wire.PermissionRequest{ID: p.ID, Tool: p.Tool, Summary: chat.Summarize(p.Tool, p.Input), Input: string(p.Input), At: p.At.UTC()}
		w.Pending.Title, w.Pending.Questions, w.Pending.Plan = chat.Asks(p.Tool, p.Input)
	}
	for _, q := range d.Queue {
		w.Queue = append(w.Queue, wire.QueuedPrompt{ID: q.ID, Text: q.Text, At: q.At.UTC()})
	}
	return w
}

// agentFromStatus is what a session's last status says about its agent,
// for a chat with no stream.
func agentFromStatus(a *pier.AgentStatus) *wire.AgentDetail {
	if a == nil {
		return nil
	}
	return &wire.AgentDetail{
		AgentStatus: wire.AgentStatus{Kind: a.Kind, State: wire.AgentState(a.State), Waiting: a.Waiting, Since: a.Since.UTC(), Last: a.Last, Queued: a.Queued},
		Queue:       []wire.QueuedPrompt{},
	}
}

// agentTopics are the stream topics a chat (or the approvals keeper)
// follows: the agent, and its permission requests when the client answers
// them.
func (s *Server) agentTopics() []string {
	if s.approvals() {
		return []string{"agent", "approvals"}
	}
	return []string{"agent"}
}

func (s *Server) approvals() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client.Approvals
}

func init() {
	watchChanges("chat", "A session's agent conversation: the newest items, then changes as the agent writes them. Parked sessions answer from this machine's copy.",
		func(ctx context.Context, s *Server, p wire.SessionRef, sub *Sub[wire.ChatChange]) (wire.ChatSnapshot, error) {
			cs, release, err := s.chat(p.ID)
			if err != nil {
				return wire.ChatSnapshot{}, err
			}
			select {
			case <-cs.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				release()
				return wire.ChatSnapshot{}, ctx.Err()
			}
			snap, unsubscribe := cs.subscribe(sub.Emit)
			go func() {
				<-sub.Done()
				unsubscribe()
				release()
			}()
			return snap, nil
		})
	handle("chat.history", "Older items of a session's chat, before the oldest one the client has.",
		func(ctx context.Context, s *Server, _ *Call, p wire.HistoryParams) (wire.HistoryPage, error) {
			s.chatMu.Lock()
			cs := s.chats[p.SessionID]
			s.chatMu.Unlock()
			if cs != nil {
				cs.mu.Lock()
				defer cs.mu.Unlock()
				return cs.feed.before(p.Before, p.Limit), nil
			}
			f, err := openChatFeed(p.SessionID)
			if err != nil {
				return wire.HistoryPage{}, err
			}
			defer f.close()
			return f.before(p.Before, p.Limit), nil
		})
	handle("chat.send", "Sends a prompt to a session's agent, once per promptId. A parked session is woken by a background job that delivers it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatSendParams) (wire.ChatSendResult, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			if sess.State != pier.StateParked {
				err = be.SendPrompt(ctx, sess, p.PromptID, p.Text, p.Mode)
				if !errors.Is(err, pier.ErrParked) {
					return wire.ChatSendResult{}, err
				}
			}
			job, err := be.StartDeliver(sess, p.PromptID, p.Text, p.Mode)
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			s.overview.kick(fastAfterAction)
			w := wireJob(job)
			return wire.ChatSendResult{Job: &w}, nil
		})
	handle("chat.interrupt", "Stops the agent's turn, as Esc does at its prompt.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) error { return be.Interrupt(ctx, sess) })
		})
	handle("chat.decide", "Answers the agent's permission request: allow or deny.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DecideParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.Decide(ctx, sess, p.RequestID, pier.Decision{
					Behavior: p.Behavior, Message: p.Message, Answers: p.Answers, Notes: p.Notes, Mode: p.Mode,
				})
			})
		})
	handle("chat.peek", "The bottom of the agent's pane as text: what it is doing, without attaching.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PeekParams) (wire.Peek, error) {
			var out wire.Peek
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				out.Text, err = be.PeekAgent(ctx, sess, p.Lines)
				return err
			})
			return out, err
		})
	handle("files.upload", "Puts a file (an image pasted into the chat) into a session; the result is its path there.",
		func(ctx context.Context, s *Server, _ *Call, p wire.UploadParams) (wire.Uploaded, error) {
			data, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				return wire.Uploaded{}, invalidParams("data isn't base64")
			}
			var path string
			err = s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				path, err = be.Upload(ctx, sess, p.Name, data)
				return err
			})
			return wire.Uploaded{Path: path}, err
		})
	handle("files.list", "The paths in a session's checkout, tracked and untracked (ignored ones left out).",
		func(ctx context.Context, s *Server, _ *Call, p wire.FileListParams) (wire.FileList, error) {
			var out wire.FileList
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				out.Paths, out.Truncated, err = be.ListFiles(ctx, sess)
				return err
			})
			if out.Paths == nil {
				out.Paths = []string{}
			}
			return out, err
		})
}

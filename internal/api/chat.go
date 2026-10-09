package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// A session's chats run in the session (its supervisor hosts the agents);
// this machine keeps a copy of each chat's log (pier.Mirror) and of the
// chat list, so a parked session still shows its chats and a reconnect
// fetches only what is new. One follower per session keeps the copies
// current while anyone watches one of its chats.

const (
	chatPage = 300 // items a snapshot or a history page carries
	// chatReadyWait bounds how long a watch waits for the session to catch
	// its copy up before answering with what it has.
	chatReadyWait = 20 * time.Second
)

// chatFeed is one chat's copy, as items.
type chatFeed struct {
	mirror *pier.Mirror
	items  []wire.ChatItem
	index  map[string]int
	// caught closes once the session has caught the copy up.
	caught     chan struct{}
	caughtOnce sync.Once
}

func openChatFeed(sessionID, chatID string) (*chatFeed, error) {
	m, err := pier.OpenMirror(sessionID, chatID)
	if err != nil {
		return nil, err
	}
	items, err := m.Items()
	if err != nil {
		m.Close()
		return nil, err
	}
	f := &chatFeed{mirror: m, caught: make(chan struct{})}
	f.reset(items)
	return f, nil
}

func (f *chatFeed) close() { f.mirror.Close() }

func (f *chatFeed) reset(items []wire.ChatItem) {
	f.items, f.index = items, map[string]int{}
	for i, it := range items {
		f.index[it.ID] = i
	}
}

// apply adds a chunk of the log and returns what the chat's watchers need
// to hear.
func (f *chatFeed) apply(ch pier.LogChunk) ([]wire.ChatChange, error) {
	reset := ch.Reset || ch.File != f.mirror.Meta().File
	if err := f.mirror.Apply(ch); err != nil {
		return nil, err
	}
	if reset {
		f.reset(nil)
	}
	start := len(f.items)
	var updated []wire.ChatItem
	seen := map[string]bool{}
	for _, it := range pier.ParseItems(ch.Data) {
		if i, ok := f.index[it.ID]; ok {
			f.items[i] = it
			if i < start && !seen[it.ID] {
				seen[it.ID] = true
				updated = append(updated, it)
			} else if i < start {
				for j := range updated {
					if updated[j].ID == it.ID {
						updated[j] = it
					}
				}
			}
			continue
		}
		f.index[it.ID] = len(f.items)
		f.items = append(f.items, it)
	}
	if reset {
		items, more := f.tail()
		return []wire.ChatChange{{Op: wire.ChatReset, Items: items, More: more}}, nil
	}
	var out []wire.ChatChange
	if len(updated) > 0 {
		out = append(out, wire.ChatChange{Op: wire.ChatUpdate, Items: updated})
	}
	if added := f.items[start:]; len(added) > 0 {
		out = append(out, wire.ChatChange{Op: wire.ChatAppend, Items: append([]wire.ChatItem(nil), added...)})
	}
	return out, nil
}

// tail is the newest page of items.
func (f *chatFeed) tail() ([]wire.ChatItem, bool) {
	from := max(len(f.items)-chatPage, 0)
	return append([]wire.ChatItem{}, f.items[from:]...), from > 0 || f.mirror.Meta().Dropped
}

// before pages older items in: up to limit items before the one with id.
// more says older ones exist (in the copy, or only in the session).
func (f *chatFeed) before(id string, limit int) (wire.HistoryPage, bool) {
	if limit <= 0 || limit > chatPage {
		limit = chatPage
	}
	end, ok := f.index[id]
	if !ok {
		return wire.HistoryPage{Items: []wire.ChatItem{}}, false
	}
	from := max(end-limit, 0)
	page := wire.HistoryPage{Items: append([]wire.ChatItem{}, f.items[from:end]...), More: from > 0 || f.mirror.Meta().Dropped}
	// The copy ran out but the session has older ones.
	return page, from == 0 && f.mirror.Meta().Dropped
}

// chatsCache is this machine's copy of a session's chat list.
func chatsCache(sessionID string) string {
	return filepath.Join(pier.MirrorDir(sessionID), "chats.json")
}

func loadChats(sessionID string) []wire.ChatInfo {
	b, err := os.ReadFile(chatsCache(sessionID))
	if err != nil {
		return nil
	}
	var out []wire.ChatInfo
	_ = json.Unmarshal(b, &out)
	return out
}

func saveChats(sessionID string, chats []wire.ChatInfo) {
	b, _ := json.Marshal(chats)
	_ = os.MkdirAll(pier.MirrorDir(sessionID), 0o700)
	tmp := chatsCache(sessionID) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, chatsCache(sessionID))
	}
}

type chatListener struct {
	chat string
	fn   func(wire.ChatChange)
}

// chatFollower follows one session's chats for their watchers.
type chatFollower struct {
	s  *Server
	id string

	mu        sync.Mutex
	feeds     map[string]*chatFeed
	chats     []wire.ChatInfo
	streaming map[string]map[string]*wire.ChatItem // chat → item id → item
	live      bool
	gap       bool // a copy fell out of step: follow again from scratch
	listeners map[int]chatListener
	lists     map[int]func(wire.ChatsChange)
	nextL     int
	refs      int
	cancel    context.CancelFunc
	// refollow ends the current follow so the next one tails a changed set
	// of chats; again says it did.
	refollow func()
	again    bool
	// tailed are the chats the current follow tails.
	tailed map[string]bool

	ready     chan struct{} // closed once the first catch-up is done (or failed)
	readyOnce sync.Once
}

// follower returns the session's chat follower, starting it if needed, and
// a release for when the caller is done with it.
func (s *Server) follower(id string) (*chatFollower, func(), error) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	f := s.chats[id]
	if f == nil {
		ctx, cancel := context.WithCancel(s.root)
		f = &chatFollower{s: s, id: id, feeds: map[string]*chatFeed{}, streaming: map[string]map[string]*wire.ChatItem{},
			listeners: map[int]chatListener{}, lists: map[int]func(wire.ChatsChange){}, cancel: cancel, ready: make(chan struct{})}
		f.chats = loadChats(id)
		for _, c := range f.chats {
			if err := f.openFeed(c.ID); err != nil {
				s.logf("chat %s/%s: %v", id, c.ID, err)
			}
		}
		s.chats[id] = f
		go f.run(ctx)
	}
	f.refs++
	var once sync.Once
	return f, func() {
		once.Do(func() {
			s.chatMu.Lock()
			f.refs--
			last := f.refs == 0
			if last {
				delete(s.chats, id)
			}
			s.chatMu.Unlock()
			if last {
				f.cancel()
			}
		})
	}, nil
}

// openFeed opens a chat's copy if it isn't open. Called with mu held (or
// before the follower is shared).
func (f *chatFollower) openFeed(chatID string) error {
	if f.feeds[chatID] != nil {
		return nil
	}
	feed, err := openChatFeed(f.id, chatID)
	if err != nil {
		return err
	}
	f.feeds[chatID] = feed
	return nil
}

// caughtCh is closed once a live chat's copy is caught up; nil when the
// session has no stream (its copy is all there is).
func (f *chatFollower) caughtCh(chatID string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live {
		return nil
	}
	if err := f.openFeed(chatID); err != nil {
		return nil
	}
	if f.info(chatID) != nil && f.refollow != nil && !f.tailed[chatID] {
		f.again = true
		f.refollow()
	}
	return f.feeds[chatID].caught
}

func (f *chatFollower) markReady() { f.readyOnce.Do(func() { close(f.ready) }) }

func (f *chatFollower) asOf(chatID string) *time.Time {
	if feed := f.feeds[chatID]; feed != nil {
		if t := feed.mirror.Meta().Synced; !t.IsZero() {
			return &t
		}
	}
	return nil
}

func (f *chatFollower) info(chatID string) *wire.ChatInfo {
	for i := range f.chats {
		if f.chats[i].ID == chatID {
			c := f.chats[i]
			return &c
		}
	}
	return nil
}

func (f *chatFollower) streamingOf(chatID string) []wire.ChatItem {
	out := []wire.ChatItem{}
	for _, it := range f.streaming[chatID] {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// emit sends a change to a chat's watchers. Called with mu held, so a
// watcher sees changes in order and never one its snapshot already holds.
func (f *chatFollower) emit(chatID string, c wire.ChatChange) {
	for _, l := range f.listeners {
		if l.chat == chatID {
			l.fn(c)
		}
	}
}

func (f *chatFollower) emitList() {
	c := wire.ChatsChange{Chats: append([]wire.ChatInfo{}, f.chats...), Live: f.live}
	for _, fn := range f.lists {
		fn(c)
	}
}

// subscribe takes a chat's snapshot and adds a watcher in one step.
func (f *chatFollower) subscribe(chatID string, fn func(wire.ChatChange)) (wire.ChatSnapshot, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.openFeed(chatID); err != nil {
		return wire.ChatSnapshot{}, nil, err
	}
	items, more := f.feeds[chatID].tail()
	snap := wire.ChatSnapshot{Items: items, More: more, Chat: f.info(chatID), Live: f.live}
	if f.live {
		snap.Streaming = f.streamingOf(chatID)
	} else {
		snap.AsOf = f.asOf(chatID)
	}
	f.nextL++
	id := f.nextL
	f.listeners[id] = chatListener{chat: chatID, fn: fn}
	return snap, func() {
		f.mu.Lock()
		delete(f.listeners, id)
		f.mu.Unlock()
	}, nil
}

func (f *chatFollower) subscribeList(fn func(wire.ChatsChange)) (wire.ChatsSnapshot, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := wire.ChatsSnapshot{Chats: append([]wire.ChatInfo{}, f.chats...), Live: f.live}
	f.nextL++
	id := f.nextL
	f.lists[id] = fn
	return snap, func() {
		f.mu.Lock()
		delete(f.lists, id)
		f.mu.Unlock()
	}
}

func (f *chatFollower) setLive(live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.live == live {
		return
	}
	f.live = live
	if !live {
		f.streaming = map[string]map[string]*wire.ChatItem{}
	}
	for chatID := range f.feeds {
		c := wire.ChatChange{Op: wire.ChatState, Chat: f.info(chatID), Live: &live}
		if !live {
			c.AsOf = f.asOf(chatID)
		}
		f.emit(chatID, c)
	}
	f.emitList()
}

// setChats takes a new chat list: from the stream, or (parked) the beacon.
func (f *chatFollower) setChats(sess pier.Session, chats []wire.ChatInfo, fromStream bool) {
	f.mu.Lock()
	old := map[string]wire.ChatInfo{}
	for _, c := range f.chats {
		old[c.ID] = c
	}
	f.chats = chats
	added := false
	keep := map[string]bool{}
	for _, c := range chats {
		keep[c.ID] = true
		if f.feeds[c.ID] == nil {
			if err := f.openFeed(c.ID); err == nil {
				added = true
			}
		}
		if prev, ok := old[c.ID]; !ok || !reflect.DeepEqual(prev, c) {
			info := c
			f.emit(c.ID, wire.ChatChange{Op: wire.ChatState, Chat: &info})
		}
		if c.State != wire.AgentWorking && c.State != wire.AgentWaiting {
			delete(f.streaming, c.ID)
		}
	}
	if fromStream {
		// Chats gone from the session were closed there.
		for id, feed := range f.feeds {
			if !keep[id] {
				feed.close()
				delete(f.feeds, id)
				os.RemoveAll(pier.ChatMirrorDir(f.id, id))
			}
		}
	}
	f.emitList()
	refollow := f.refollow
	if added && fromStream && refollow != nil {
		f.again = true
	}
	f.mu.Unlock()
	saveChats(f.id, chats)
	for _, c := range chats {
		f.s.notices.chat(sess, c)
	}
	if added && fromStream && refollow != nil {
		refollow()
	}
}

// run keeps the copies current while the session runs, and waits for it
// to run while it doesn't.
func (f *chatFollower) run(ctx context.Context) {
	defer func() {
		f.mu.Lock()
		for _, feed := range f.feeds {
			feed.close()
		}
		f.mu.Unlock()
	}()
	for attempt := 0; ctx.Err() == nil; {
		be, sess, err := f.s.sessionFor(ctx, f.id)
		if err != nil || !running(sess) || !be.HasKey(sess) {
			if err == nil && !f.isLive() {
				f.fromBeacon(sess)
			}
			f.setLive(false)
			f.markReady()
			if !sleepCtx(ctx, chatRecheck) {
				return
			}
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		f.mu.Lock()
		var tails []pier.Tail
		f.tailed = map[string]bool{}
		for id, feed := range f.feeds {
			meta := feed.mirror.Meta()
			tails = append(tails, pier.Tail{Chat: id, File: meta.File, From: meta.Next})
			f.tailed[id] = true
		}
		f.refollow = cancel
		f.mu.Unlock()
		started := time.Now()
		err = be.FollowChats(fctx, sess, []string{"chats"}, tails, func(ev string, data json.RawMessage) {
			f.onEvent(sess, ev, data, cancel)
		})
		cancel()
		f.mu.Lock()
		f.refollow = nil
		again := f.gap || f.again
		f.gap, f.again = false, false
		f.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if again {
			continue // a new chat to tail, or a copy to start over
		}
		f.setLive(false)
		f.markReady() // a first connect that failed still lets watchers see the copy
		if err != nil {
			f.s.logf("chats of %s: %v", sess.Name, err)
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

// fromBeacon fills the list of a session with no stream from what its last
// beacon said, for the chats this machine has no record of.
func (f *chatFollower) fromBeacon(sess pier.Session) {
	f.mu.Lock()
	have := map[string]bool{}
	for _, c := range f.chats {
		have[c.ID] = true
	}
	chats := append([]wire.ChatInfo{}, f.chats...)
	changed := false
	for _, c := range sess.Chats {
		if have[c.ID] {
			for i := range chats {
				if chats[i].ID == c.ID {
					chats[i].ChatSummary = c
				}
			}
			continue
		}
		chats = append(chats, wire.ChatInfo{ChatSummary: c, Queue: []wire.QueuedPrompt{}})
		changed = true
	}
	f.mu.Unlock()
	if changed || len(sess.Chats) > 0 {
		f.setChats(sess, chats, false)
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

func (f *chatFollower) isLive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live
}

func (f *chatFollower) onEvent(sess pier.Session, ev string, data json.RawMessage, stop func()) {
	switch ev {
	case "chats":
		chats, err := pier.ParseChats(data)
		if err != nil {
			return
		}
		f.setLive(true)
		f.setChats(sess, chats, true)
		f.markReady()
	case "log":
		var ch pier.LogChunk
		if json.Unmarshal(data, &ch) != nil {
			return
		}
		f.mu.Lock()
		feed := f.feeds[ch.Chat]
		if feed == nil {
			f.mu.Unlock()
			return
		}
		changes, err := feed.apply(ch)
		if err != nil {
			// Start the copy over: the next follow sends it all.
			f.s.logf("chat %s/%s: %v — reading its log again", sess.Name, ch.Chat, err)
			feed.mirror.Reset()
			feed.reset(nil)
			f.gap = true
			f.mu.Unlock()
			stop()
			return
		}
		for _, c := range changes {
			for _, it := range c.Items {
				delete(f.streaming[ch.Chat], it.ID)
			}
			f.emit(ch.Chat, c)
		}
		if ch.EOF {
			feed.caughtOnce.Do(func() { close(feed.caught) })
		}
		f.mu.Unlock()
	case "streaming":
		var m struct {
			Chat  string          `json:"chat"`
			Items []wire.ChatItem `json:"items"`
		}
		if json.Unmarshal(data, &m) != nil {
			return
		}
		f.mu.Lock()
		set := map[string]*wire.ChatItem{}
		for i := range m.Items {
			it := m.Items[i]
			set[it.ID] = &it
		}
		f.streaming[m.Chat] = set
		f.emit(m.Chat, wire.ChatChange{Op: wire.ChatStreaming, Items: f.streamingOf(m.Chat)})
		f.mu.Unlock()
	case "delta":
		var m struct {
			Chat   string           `json:"chat"`
			Deltas []wire.ChatDelta `json:"deltas"`
		}
		if json.Unmarshal(data, &m) != nil {
			return
		}
		f.mu.Lock()
		set := f.streaming[m.Chat]
		if set == nil {
			set = map[string]*wire.ChatItem{}
			f.streaming[m.Chat] = set
		}
		var keep []wire.ChatDelta
		feed := f.feeds[m.Chat]
		for _, d := range m.Deltas {
			if feed != nil {
				if _, done := feed.index[d.ID]; done {
					continue // its finished item is here already
				}
			}
			it := set[d.ID]
			if it == nil {
				it = &wire.ChatItem{ID: d.ID, Kind: d.Kind, Time: time.Now().UTC()}
				set[d.ID] = it
			}
			it.Text += d.Text
			keep = append(keep, d)
		}
		if len(keep) > 0 {
			f.emit(m.Chat, wire.ChatChange{Op: wire.ChatDeltas, Deltas: keep})
		}
		f.mu.Unlock()
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

// staged is where a parked session's attachments wait on this Mac.
func stagedDir() string { return filepath.Join(os.TempDir(), "pier-staged") }

const stagedPrefix = "staged:"

// splitAttachments separates attachments already in the session from
// those still on this Mac.
func splitAttachments(as []wire.Attachment) (inSession []wire.Attachment, local []string) {
	for _, a := range as {
		if p, ok := cutPrefix(a.Path, stagedPrefix); ok {
			local = append(local, p)
			continue
		}
		inSession = append(inSession, a)
	}
	return inSession, local
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

// uploadStaged puts a running session's staged files in it.
func uploadStaged(ctx context.Context, be Backend, sess pier.Session, local []string) ([]wire.Attachment, error) {
	var out []wire.Attachment
	for _, p := range local {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		path, err := be.Upload(ctx, sess, filepath.Base(p), data)
		if err != nil {
			return nil, err
		}
		out = append(out, wire.Attachment{Name: filepath.Base(p), Path: path})
		os.Remove(p)
	}
	return out, nil
}

func init() {
	watchChanges("chats", "A session's chats: their state, what they wait on, their queues. Parked sessions answer from this machine's copy.",
		func(ctx context.Context, s *Server, p wire.SessionRef, sub *Sub[wire.ChatsChange]) (wire.ChatsSnapshot, error) {
			f, release, err := s.follower(p.ID)
			if err != nil {
				return wire.ChatsSnapshot{}, err
			}
			select {
			case <-f.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				release()
				return wire.ChatsSnapshot{}, ctx.Err()
			}
			snap, unsubscribe := f.subscribeList(sub.Emit)
			go func() {
				<-sub.Done()
				unsubscribe()
				release()
			}()
			return snap, nil
		})
	watchChanges("chat", "A chat's conversation: the newest items, then changes as the agent writes them (text streams in as deltas). Parked sessions answer from this machine's copy.",
		func(ctx context.Context, s *Server, p wire.ChatRef, sub *Sub[wire.ChatChange]) (wire.ChatSnapshot, error) {
			f, release, err := s.follower(p.SessionID)
			if err != nil {
				return wire.ChatSnapshot{}, err
			}
			select {
			case <-f.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				release()
				return wire.ChatSnapshot{}, ctx.Err()
			}
			// A live chat answers once its copy is caught up.
			if caught := f.caughtCh(p.ChatID); caught != nil {
				select {
				case <-caught:
				case <-time.After(chatReadyWait):
				case <-ctx.Done():
					release()
					return wire.ChatSnapshot{}, ctx.Err()
				}
			}
			snap, unsubscribe, err := f.subscribe(p.ChatID, sub.Emit)
			if err != nil {
				release()
				return wire.ChatSnapshot{}, err
			}
			go func() {
				<-sub.Done()
				unsubscribe()
				release()
			}()
			return snap, nil
		})
	handle("chat.history", "Older items of a chat, before the oldest one the client has.",
		func(ctx context.Context, s *Server, _ *Call, p wire.HistoryParams) (wire.HistoryPage, error) {
			s.chatMu.Lock()
			f := s.chats[p.SessionID]
			s.chatMu.Unlock()
			var page wire.HistoryPage
			var fromSession bool
			if f != nil {
				f.mu.Lock()
				if err := f.openFeed(p.ChatID); err != nil {
					f.mu.Unlock()
					return wire.HistoryPage{}, err
				}
				page, fromSession = f.feeds[p.ChatID].before(p.Before, p.Limit)
				f.mu.Unlock()
			} else {
				feed, err := openChatFeed(p.SessionID, p.ChatID)
				if err != nil {
					return wire.HistoryPage{}, err
				}
				page, fromSession = feed.before(p.Before, p.Limit)
				feed.close()
			}
			if !fromSession || len(page.Items) > 0 {
				return page, nil
			}
			// Older than this Mac keeps: ask the session, when it runs.
			var items []wire.ChatItem
			var more bool
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				if !running(sess) {
					return nil
				}
				items, more, err = be.ChatPage(ctx, sess, p.ChatID, p.Before, p.Limit)
				return err
			})
			if items == nil {
				items = []wire.ChatItem{}
			}
			return wire.HistoryPage{Items: items, More: more}, err
		})
	handle("chat.create", "Starts a chat in a session, once per chatId. A parked session is woken by a background job that starts it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatCreateParams) (wire.ChatCreated, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.ChatCreated{}, err
			}
			req := pier.ChatRequest{ID: p.ChatID, Agent: p.Agent, Model: p.Model, Effort: p.Effort, Mode: p.Mode, Title: p.Title}
			var local []string
			if p.Prompt != nil {
				prompt := *p.Prompt
				prompt.Attachments, local = splitAttachments(prompt.Attachments)
				req.Prompt = &prompt
			}
			if sess.State != pier.StateParked {
				if len(local) > 0 {
					up, err := uploadStaged(ctx, be, sess, local)
					if err != nil {
						return wire.ChatCreated{}, err
					}
					req.Prompt.Attachments = append(req.Prompt.Attachments, up...)
					local = nil
				}
				id, err := be.CreateChat(ctx, sess, req)
				if err == nil {
					s.overview.kick(fastAfterAction)
					return wire.ChatCreated{ChatID: id}, nil
				}
				if err != pier.ErrParked {
					return wire.ChatCreated{}, err
				}
			}
			if req.Prompt == nil {
				req.Prompt = &wire.ChatPrompt{}
			}
			if req.ID == "" || req.Prompt.ID == "" {
				return wire.ChatCreated{}, invalidParams("a parked session's new chat needs chatId and a prompt with an id")
			}
			job, err := be.StartDeliver(sess, pier.Delivery{Chat: req, Prompt: *req.Prompt, Files: local})
			if err != nil {
				return wire.ChatCreated{}, err
			}
			s.overview.kick(fastAfterAction)
			w := wireJob(job)
			return wire.ChatCreated{ChatID: req.ID, Job: &w}, nil
		})
	handle("chat.send", "Sends a prompt to a chat, once per prompt id. A parked session is woken by a background job that delivers it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatSendParams) (wire.ChatSendResult, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			prompt := p.Prompt
			var local []string
			prompt.Attachments, local = splitAttachments(prompt.Attachments)
			if sess.State != pier.StateParked {
				if len(local) > 0 {
					up, err := uploadStaged(ctx, be, sess, local)
					if err != nil {
						return wire.ChatSendResult{}, err
					}
					prompt.Attachments = append(prompt.Attachments, up...)
					local = nil
				}
				err = be.SendPrompt(ctx, sess, p.ChatID, prompt, p.Mode)
				if err != pier.ErrParked {
					return wire.ChatSendResult{}, err
				}
			}
			job, err := be.StartDeliver(sess, pier.Delivery{Chat: pier.ChatRequest{ID: p.ChatID}, Prompt: prompt, Files: local, Mode: p.Mode})
			if err != nil {
				return wire.ChatSendResult{}, err
			}
			s.overview.kick(fastAfterAction)
			w := wireJob(job)
			return wire.ChatSendResult{Job: &w}, nil
		})
	handle("chat.dequeue", "Takes back a prompt still waiting in a chat's queue.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DequeueParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.Dequeue(ctx, sess, p.ChatID, p.PromptID) })
		})
	handle("chat.interrupt", "Stops a chat's turn.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.Interrupt(ctx, sess, p.ChatID) })
		})
	handle("chat.decide", "Answers what a chat waits on: a tool use, its questions, its plan.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DecideParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.Decide(ctx, sess, p.ChatID, p.RequestID, pier.Decision{
					Behavior: p.Behavior, Message: p.Message, Answers: p.Answers, Notes: p.Notes, Mode: p.Mode,
				})
			})
		})
	handle("chat.configure", "Changes a chat's model, effort, permission mode or title as it goes on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatConfigureParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.ConfigureChat(ctx, sess, p.ChatID, pier.ChatConfig{Model: p.Model, Effort: p.Effort, Mode: p.Mode, Title: p.Title})
			})
		})
	handle("chat.close", "Stops a chat and puts it away.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error { return be.CloseChat(ctx, sess, p.ChatID) })
		})
	handle("chat.takeover", "Opens a terminal window in the session that continues a chat in its agent's own interface; the chat waits, and takes back what happens there when it exits. The result is the window to open a terminal tab on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ChatRef) (wire.WindowRef, error) {
			var w wire.WindowRef
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				w.WindowID, err = be.ChatWindow(ctx, sess, p.ChatID, true)
				return err
			})
			return w, err
		})
	handle("chats.window", "Opens the session's chats, live and read-only, in a terminal window of their own (made if missing); the result is the window to open a terminal tab on.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (wire.WindowRef, error) {
			var w wire.WindowRef
			err := s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) (err error) {
				w.WindowID, err = be.ChatWindow(ctx, sess, "", false)
				return err
			})
			return w, err
		})
	handle("agents.models", "What an agent offers — models with their efforts, permission modes — as a running session's agent says, else as last heard.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ModelsParams) (wire.AgentModels, error) {
			be, err := s.backend()
			if err != nil {
				return wire.AgentModels{}, err
			}
			if p.Agent != "claude" && p.Agent != "codex" {
				return wire.AgentModels{}, invalidParams("agent is claude or codex")
			}
			var sess *pier.Session
			if p.SessionID != "" {
				if got, err := s.overview.session(ctx, p.SessionID); err == nil && running(got) {
					sess = &got
				}
			}
			return be.Models(ctx, sess, p.Agent)
		})
	handle("files.upload", "Puts a file attached to a prompt into a session; a parked session's waits on this Mac until the prompt wakes it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.UploadParams) (wire.Attachment, error) {
			data, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				return wire.Attachment{}, invalidParams("data isn't base64")
			}
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.Attachment{}, err
			}
			name := filepath.Base(p.Name)
			if !running(sess) {
				dir := filepath.Join(stagedDir(), pier.NewID())
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return wire.Attachment{}, err
				}
				local := filepath.Join(dir, name)
				if err := os.WriteFile(local, data, 0o600); err != nil {
					return wire.Attachment{}, err
				}
				return wire.Attachment{Name: name, Path: stagedPrefix + local, Mime: p.Mime}, nil
			}
			path, err := be.Upload(ctx, sess, name, data)
			return wire.Attachment{Name: name, Path: path, Mime: p.Mime}, err
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

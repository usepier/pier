package api

import (
	"context"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// A session's checkout changes are pushed by its supervisor while anyone
// watches them; one follower per session passes them on, and keeps what a
// parked session shows (its changes, diffs, changed files, file list) on
// this machine as they come.

// gitRecheck is how often a watch of a session that can't be read now
// looks again (it wakes, a key turns up).
var gitRecheck = 10 * time.Second

type gitFollower struct {
	s  *Server
	id string

	mu        sync.Mutex
	state     *wire.Changes
	err       error
	listeners map[int]func(wire.Changes)
	nextL     int
	refs      int
	cancel    context.CancelFunc
	ready     chan struct{} // closed once there is a state (or an error)
	readyOnce sync.Once
	// keep holds the newest state still to be kept (older ones are
	// skipped); kick wakes the keeper.
	keep     *pier.GitState
	keepSess pier.Session
	keepBe   Backend
	kick     chan struct{}
}

func (s *Server) gitFollower(id string) (*gitFollower, func()) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	f := s.gits[id]
	if f == nil {
		ctx, cancel := context.WithCancel(s.root)
		f = &gitFollower{s: s, id: id, listeners: map[int]func(wire.Changes){}, cancel: cancel,
			ready: make(chan struct{}), kick: make(chan struct{}, 1)}
		s.gits[id] = f
		go f.run(ctx)
		go f.keeper(ctx)
	}
	f.refs++
	var once sync.Once
	return f, func() {
		once.Do(func() {
			s.chatMu.Lock()
			f.refs--
			last := f.refs == 0
			if last {
				delete(s.gits, id)
			}
			s.chatMu.Unlock()
			if last {
				f.cancel()
			}
		})
	}
}

func (f *gitFollower) set(c wire.Changes) {
	f.mu.Lock()
	f.state, f.err = &c, nil
	for _, l := range f.listeners {
		l(c)
	}
	f.mu.Unlock()
	f.readyOnce.Do(func() { close(f.ready) })
}

// fail records why there is no state, unless there already is one.
func (f *gitFollower) fail(err error) {
	f.mu.Lock()
	if f.state == nil {
		f.err = err
	}
	f.mu.Unlock()
	f.readyOnce.Do(func() { close(f.ready) })
}

// offline shows what this machine kept, once the session can't be read.
func (f *gitFollower) offline(be Backend, sess pier.Session) {
	f.mu.Lock()
	shown := f.state != nil && !f.state.Live
	f.mu.Unlock()
	if shown {
		return
	}
	if c, at, ok := pier.CachedChanges(sess.ID); ok {
		w := wireChanges(c, nil)
		w.AsOf = asOf(at)
		f.set(w)
		return
	}
	f.fail(unreadable(be, sess))
}

func (f *gitFollower) run(ctx context.Context) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		be, sess, err := f.s.sessionFor(ctx, f.id)
		wait := gitRecheck
		switch {
		case err != nil:
			f.fail(err)
		case !readable(be, sess):
			f.offline(be, sess)
		default:
			err = be.FollowGit(ctx, sess, func(g pier.GitState) {
				attempt = 0
				if g.Changes == nil {
					f.fail(g.Err)
					return
				}
				w := wireChanges(*g.Changes, g.Stamps)
				w.Live = true
				f.set(w)
				f.queueKeep(be, sess, g)
			})
			if ctx.Err() != nil {
				return
			}
			f.s.logf("following %s's checkout: %v", f.id, err)
			f.fail(err)
			// The connection dropped: try again, gently.
			wait = time.Duration(min(attempt+1, 6)) * 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (f *gitFollower) queueKeep(be Backend, sess pier.Session, g pier.GitState) {
	f.mu.Lock()
	f.keep, f.keepBe, f.keepSess = &g, be, sess
	f.mu.Unlock()
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// keeper keeps the newest state for when the session is parked, one at a
// time.
func (f *gitFollower) keeper(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.kick:
		}
		f.mu.Lock()
		g, be, sess := f.keep, f.keepBe, f.keepSess
		f.keep = nil
		f.mu.Unlock()
		if g == nil {
			continue
		}
		if err := be.KeepSnapshot(ctx, sess, *g); err != nil && ctx.Err() == nil {
			f.s.logf("keeping %s's checkout: %v", f.id, err)
		}
	}
}

// subscribe takes the current state and adds a watcher in one step.
func (f *gitFollower) subscribe(fn func(wire.Changes)) (*wire.Changes, error, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextL++
	id := f.nextL
	f.listeners[id] = fn
	return f.state, f.err, func() {
		f.mu.Lock()
		delete(f.listeners, id)
		f.mu.Unlock()
	}
}

func init() {
	watch("git", "Everything a session's branch changed against its base, pushed as it changes. A session that can't be read answers with what this machine kept (live false, asOf), and goes live once it can be.",
		func(ctx context.Context, s *Server, p wire.SessionRef, sub *Sub[wire.Changes]) (wire.Changes, error) {
			f, release := s.gitFollower(p.ID)
			go func() {
				<-ctx.Done()
				release()
			}()
			select {
			case <-f.ready:
			case <-time.After(chatReadyWait):
			case <-ctx.Done():
				return wire.Changes{}, ctx.Err()
			}
			state, err, unsub := f.subscribe(sub.Emit)
			go func() {
				<-ctx.Done()
				unsub()
			}()
			if state != nil {
				return *state, nil
			}
			if err == nil {
				err = errUnreachable
			}
			return wire.Changes{}, err
		})
	watch("search", "Finds text in a session's checkout (literal, or a regular expression), sending matches as they're found; the last event says done.",
		func(ctx context.Context, s *Server, p wire.SearchParams, sub *Sub[wire.SearchResult]) (wire.SearchResult, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.SearchResult{}, err
			}
			if !readable(be, sess) {
				return wire.SearchResult{}, unreadable(be, sess)
			}
			go func() {
				q := pier.SearchQuery{Query: p.Query, Regex: p.Regex, CaseSensitive: p.CaseSensitive}
				err := be.Search(ctx, sess, q, func(ms []pier.Match, done, truncated bool) {
					out := wire.SearchResult{Matches: make([]wire.SearchMatch, len(ms)), Done: done, Truncated: truncated}
					for i, m := range ms {
						out.Matches[i] = wire.SearchMatch(m)
					}
					sub.Emit(out)
				})
				if err != nil && ctx.Err() == nil {
					sub.Emit(wire.SearchResult{Matches: []wire.SearchMatch{}, Done: true, Error: errorData(err)})
				}
			}()
			return wire.SearchResult{Matches: []wire.SearchMatch{}}, nil
		})
}

package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"sync"

	"github.com/usepier/pier/pkg/pier/wire"
)

type subscription struct {
	id     string
	cancel context.CancelFunc
}

// maxQueued bounds the updates a subscription holds for a client that
// isn't reading them; past it, the client is told to start over.
const maxQueued = 4096

// Sub is the sending end of one subscription. Updates queue per
// subscription and go out in order from its own goroutine, so whoever
// emits never waits on the client.
type Sub[D any] struct {
	s      *Server
	id     string
	topic  string
	ctx    context.Context
	opened chan struct{} // closed once the watch reply is on the wire
	wake   chan struct{}

	mu     sync.Mutex
	queue  []D
	lagged bool // the queue overflowed: what's left is one skip
	seq    int
}

func newSub[D any](s *Server, id, topic string, ctx context.Context) *Sub[D] {
	u := &Sub[D]{s: s, id: id, topic: topic, ctx: ctx, opened: make(chan struct{}), wake: make(chan struct{}, 1)}
	go u.pump()
	return u
}

// Emit queues one update. Updates emitted before the watch reply went out
// wait for it, so a client never sees an event for a sub it doesn't know.
func (u *Sub[D]) Emit(d D) {
	u.mu.Lock()
	switch {
	case u.ctx.Err() != nil || u.lagged:
	case len(u.queue) >= maxQueued:
		// A client this far behind starts over from a fresh snapshot.
		u.queue, u.lagged = nil, true
	default:
		u.queue = append(u.queue, d)
	}
	u.mu.Unlock()
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// pump sends what was queued, in order.
func (u *Sub[D]) pump() {
	select {
	case <-u.opened:
	case <-u.ctx.Done():
		return
	}
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-u.wake:
		}
		for {
			u.mu.Lock()
			batch, lagged := u.queue, u.lagged
			u.queue = nil
			u.mu.Unlock()
			if lagged {
				// A sequence number skipped: the client resubscribes.
				u.seq += 2
				u.s.notify("sub.event", wire.SubEvent{Sub: u.id, Seq: u.seq, Topic: u.topic})
				return
			}
			if len(batch) == 0 {
				break
			}
			for _, d := range batch {
				if u.ctx.Err() != nil {
					return
				}
				u.seq++
				u.s.notify("sub.event", wire.SubEvent{Sub: u.id, Seq: u.seq, Topic: u.topic, Data: d})
			}
		}
	}
}

// Done is closed when the client cancels the subscription or goes away.
func (u *Sub[D]) Done() <-chan struct{} { return u.ctx.Done() }

// watch registers <topic>.watch. start returns the first snapshot and may
// keep emitting through the Sub until its Done closes — from goroutines it
// starts; start itself must return promptly.
func watch[P, D any](topic, doc string, start func(ctx context.Context, s *Server, p P, sub *Sub[D]) (D, error)) {
	watchChanges(topic, doc, start)
}

// watchChanges is watch for a topic whose updates are changes to its
// snapshot (type S) rather than whole new ones.
func watchChanges[P, S, D any](topic, doc string, start func(ctx context.Context, s *Server, p P, sub *Sub[D]) (S, error)) {
	name := topic + ".watch"
	methods[name] = &method{
		Name: name, Doc: doc, Topic: topic,
		Params: reflect.TypeFor[P](), Result: reflect.TypeFor[wire.WatchResult[S]](), Data: reflect.TypeFor[D](),
		run: func(ctx context.Context, s *Server, c *Call, raw json.RawMessage) (any, error) {
			var p P
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, invalidParams(err.Error())
				}
			}
			s.mu.Lock()
			s.nextSub++
			id := topic + "-" + strconv.Itoa(s.nextSub)
			subCtx, cancel := context.WithCancel(s.root)
			s.subs[id] = &subscription{id: id, cancel: cancel}
			s.mu.Unlock()
			sub := newSub[D](s, id, topic, subCtx)
			snap, err := start(subCtx, s, p, sub)
			if err != nil {
				s.cancelSub(id)
				return nil, err
			}
			return afterReply{result: wire.WatchResult[S]{Sub: id, Snapshot: snap}, then: func() { close(sub.opened) }}, nil
		},
	}
}

func (s *Server) cancelSub(id string) {
	s.mu.Lock()
	sub := s.subs[id]
	delete(s.subs, id)
	s.mu.Unlock()
	if sub != nil {
		sub.cancel()
	}
}

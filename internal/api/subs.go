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

// Sub is the sending end of one subscription.
type Sub[D any] struct {
	s      *Server
	id     string
	topic  string
	ctx    context.Context
	opened chan struct{} // closed once the watch reply is on the wire

	mu  sync.Mutex
	seq int
}

// Emit sends one update. Updates produced before the watch reply went out
// wait for it, so a client never sees an event for a sub it doesn't know.
func (u *Sub[D]) Emit(d D) {
	select {
	case <-u.opened:
	case <-u.ctx.Done():
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ctx.Err() != nil {
		return
	}
	u.seq++
	u.s.notify("sub.event", wire.SubEvent{Sub: u.id, Seq: u.seq, Topic: u.topic, Data: d})
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
			sub := &Sub[D]{s: s, id: id, topic: topic, ctx: subCtx, opened: make(chan struct{})}
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

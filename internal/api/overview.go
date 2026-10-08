package api

import (
	"context"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// Refresh cadence. The VM changes state on its own (idle, working, parking
// itself), so the list is polled; how often depends on whether anything is
// changing and whether anyone is looking.
const (
	pollFast   = 5 * time.Second
	pollSlow   = 30 * time.Second
	pollHidden = 60 * time.Second
	// sweepEvery spaces out the stray sweep, which destroys instances: the
	// list refreshes every few seconds, cleanup doesn't need to.
	sweepEvery = 10 * time.Minute
	quotaEvery = 5 * time.Minute
	// fastAfterAction is how many quick rounds follow an action, until its
	// effect (a new row, a resumed session) shows.
	fastAfterAction = 12
)

// overviewPoller refreshes the overview for every overview subscriber and
// keeps the latest listing for actions that look sessions up by id.
type overviewPoller struct {
	s *Server

	mu        sync.Mutex
	listeners map[int]func(wire.Overview)
	nextL     int
	cancel    context.CancelFunc
	done      chan struct{}
	kickc     chan struct{}
	fast      int

	have     bool
	last     wire.Overview
	sessions []pier.Session // the latest listing, enriched when available
	at       time.Time      // when sessions was read
	sweptAt  time.Time
	quotaAt  time.Time
	quota    *wire.Quota
	lastErr  error

	round sync.Mutex // one refresh at a time
}

func newOverviewPoller(s *Server) *overviewPoller {
	return &overviewPoller{s: s, listeners: map[int]func(wire.Overview){}, kickc: make(chan struct{}, 1)}
}

// subscribe adds a listener and starts the loop if it isn't running. The
// returned func removes the listener; the loop stops with the last one.
func (p *overviewPoller) subscribe(fn func(wire.Overview)) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextL++
	id := p.nextL
	p.listeners[id] = fn
	if p.cancel == nil {
		ctx, cancel := context.WithCancel(p.s.root)
		p.cancel, p.done = cancel, make(chan struct{})
		go p.loop(ctx, p.done)
	}
	return func() {
		p.mu.Lock()
		delete(p.listeners, id)
		var cancel context.CancelFunc
		if len(p.listeners) == 0 {
			cancel, p.cancel = p.cancel, nil
		}
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

func (p *overviewPoller) stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// kick refreshes now and keeps the next n rounds fast.
func (p *overviewPoller) kick(n int) {
	p.mu.Lock()
	p.fast = max(p.fast, n)
	p.mu.Unlock()
	select {
	case p.kickc <- struct{}{}:
	default:
	}
}

func (p *overviewPoller) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		p.mu.Lock()
		stale := time.Since(p.at) >= pollFast/2
		p.mu.Unlock()
		if stale { // a watch that just refreshed doesn't need another round
			p.refresh(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval()):
		case <-p.kickc:
			p.mu.Lock()
			p.at = time.Time{} // a kick always refreshes
			p.mu.Unlock()
		}
	}
}

func (p *overviewPoller) interval() time.Duration {
	p.s.mu.Lock()
	focused := p.s.activity.Focused
	p.s.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fast > 0 {
		p.fast--
		return pollFast
	}
	if inFlight(p.sessions) {
		return pollFast
	}
	if !focused {
		return pollHidden
	}
	return pollSlow
}

// inFlight reports whether something is changing on its own (a create, a
// setup, a pool fill, a delete), so the list refreshes fast.
func inFlight(sessions []pier.Session) bool {
	for _, s := range sessions {
		switch {
		case s.State == pier.StateCreating, s.State == pier.StateDeleting,
			s.Setup == "running",
			s.PoolGen != "" && s.State != pier.StateParked && s.State != pier.StateDead:
			return true
		}
	}
	return false
}

// refresh runs one round: list (sweeping when due), publish, read the
// running sessions' statuses, publish again. A failed list publishes the
// last good data with the error attached.
func (p *overviewPoller) refresh(ctx context.Context) {
	p.round.Lock()
	defer p.round.Unlock()
	be, err := p.s.backend()
	if err != nil {
		p.fail(err)
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	all, err := be.ListSessions(lctx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			p.fail(err)
		}
		return
	}
	p.mu.Lock()
	sweep := time.Since(p.sweptAt) >= sweepEvery
	prev := p.sessions
	p.mu.Unlock()
	if sweep {
		all = be.Sweep(ctx, all)
		p.mu.Lock()
		p.sweptAt = time.Now()
		p.mu.Unlock()
	}
	p.quotaDue(ctx, be)
	fresh := append([]pier.Session(nil), all...)
	p.publish(be, pier.CarryStatus(prev, all), false)
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	enriched := be.Enrich(ectx, fresh)
	cancel()
	if ctx.Err() != nil {
		return
	}
	p.publish(be, enriched, true)
	p.s.notices.poll(enriched)
	p.s.mu.Lock()
	focused := p.s.activity.Focused
	p.s.mu.Unlock()
	if p.s.prs.refreshIfDue(ctx, be, enriched, focused) {
		p.publish(be, enriched, true)
	}
	if sweep {
		ids := make([]string, len(enriched))
		for i, s := range enriched {
			ids[i] = s.ID
		}
		pier.PruneMirrors(ids)
	}
}

func (p *overviewPoller) quotaDue(ctx context.Context, be Backend) {
	p.mu.Lock()
	due := time.Since(p.quotaAt) >= quotaEvery
	p.mu.Unlock()
	if !due {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	q, err := be.Headroom(qctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotaAt = time.Now()
	if err == nil && q.Limit > 0 {
		p.quota = &wire.Quota{Used: q.Used, Limit: q.Limit, Detail: q.Detail}
	}
}

func (p *overviewPoller) publish(be Backend, sessions []pier.Session, enriched bool) {
	p.mu.Lock()
	quota := p.quota
	p.mu.Unlock()
	o := buildOverview(be, sessions, quota, enriched, p.s.prs.snapshot())
	p.mu.Lock()
	p.have, p.last, p.sessions, p.at, p.lastErr = true, o, sessions, time.Now(), nil
	fns := make([]func(wire.Overview), 0, len(p.listeners))
	for _, fn := range p.listeners {
		fns = append(fns, fn)
	}
	p.mu.Unlock()
	for _, fn := range fns {
		fn(o)
	}
}

// lastError is the error of the last failed refresh, nil after a good one.
func (p *overviewPoller) lastError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

func (p *overviewPoller) fail(err error) {
	p.mu.Lock()
	p.lastErr = err
	o := p.last
	o.Error = errorData(err)
	o.UpdatedAt = time.Now().UTC()
	if o.Sessions == nil {
		o.Sessions, o.Repos, o.Checkouts = []wire.Session{}, []wire.Repo{}, []wire.Checkout{}
	}
	p.last, p.have = o, true
	fns := make([]func(wire.Overview), 0, len(p.listeners))
	for _, fn := range p.listeners {
		fns = append(fns, fn)
	}
	p.mu.Unlock()
	for _, fn := range fns {
		fn(o)
	}
}

// current returns the latest overview, refreshing first when there is none
// or it is older than maxAge.
func (p *overviewPoller) current(ctx context.Context, maxAge time.Duration) wire.Overview {
	p.mu.Lock()
	have, at, o := p.have, p.at, p.last
	p.mu.Unlock()
	if have && time.Since(at) <= maxAge {
		return o
	}
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// session finds a session by id in the latest listing, listing afresh when
// it isn't there (a row created moments ago).
func (p *overviewPoller) session(ctx context.Context, id string) (pier.Session, error) {
	p.mu.Lock()
	for _, s := range p.sessions {
		if s.ID == id {
			p.mu.Unlock()
			return s, nil
		}
	}
	p.mu.Unlock()
	be, err := p.s.backend()
	if err != nil {
		return pier.Session{}, err
	}
	all, err := be.ListSessions(ctx)
	if err != nil {
		return pier.Session{}, err
	}
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
	}
	return pier.Session{}, pier.ErrNotFound
}

func init() {
	handle("overview.get", "The sidebar, Home and cockpit data, refreshed if older than a few seconds.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.Overview, error) {
			return s.overview.current(ctx, 3*time.Second), nil
		})
	watch("overview", "The overview, pushed after every refresh.",
		func(ctx context.Context, s *Server, _ wire.Empty, sub *Sub[wire.Overview]) (wire.Overview, error) {
			snap := s.overview.current(ctx, 3*time.Second)
			unsubscribe := s.overview.subscribe(sub.Emit)
			go func() {
				<-sub.Done()
				unsubscribe()
			}()
			return snap, nil
		})
}

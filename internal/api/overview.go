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
	first    chan struct{} // closed once there is an overview to show
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
	return &overviewPoller{s: s, listeners: map[int]func(wire.Overview){}, kickc: make(chan struct{}, 1), first: make(chan struct{})}
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
		go p.s.prs.run(ctx)
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

// configCheck is how often the loop looks for a change to the config file
// another process made (a bake's finish recording its image, a setting
// changed elsewhere), to show it at once rather than on the next round.
var configCheck = 2 * time.Second

func (p *overviewPoller) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	cfg := pier.ConfigStamp()
	check := time.NewTicker(configCheck)
	defer check.Stop()
	for {
		p.mu.Lock()
		stale := time.Since(p.at) >= pollFast/2
		p.mu.Unlock()
		if stale { // a watch that just refreshed doesn't need another round
			p.refresh(ctx)
		}
		next := time.After(p.interval())
	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-next:
				break wait
			case <-check.C:
				if now := pier.ConfigStamp(); now != cfg {
					cfg = now
					p.mu.Lock()
					p.at = time.Time{}
					p.mu.Unlock()
					break wait
				}
			case <-p.kickc:
				p.mu.Lock()
				p.at = time.Time{} // a kick always refreshes
				p.mu.Unlock()
				break wait
			}
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
	p.s.fetchCheckouts(be)
	fresh := append([]pier.Session(nil), all...)
	p.publish(be, pier.CarryStatus(prev, all), false)
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	enriched := be.Enrich(ectx, fresh)
	cancel()
	if ctx.Err() != nil {
		return
	}
	p.publish(be, enriched, true)
	if p.s.prs.unchecked(enriched) {
		p.s.prs.kick() // a session GitHub wasn't asked about yet (new, or its branch changed)
	}
	own := ownSessions(enriched)
	p.s.notices.poll(own)
	p.s.keepSnapshots(be, own)
	p.s.refill(be, enriched)
	if sweep {
		ids := make([]string, len(enriched))
		for i, s := range enriched {
			ids[i] = s.ID
		}
		pier.PruneMirrors(ids)
	}
}

// ownSessions leaves out the machines a bake runs on: listed like sessions,
// but no one's to notify about or keep a snapshot of.
func ownSessions(all []pier.Session) []pier.Session {
	out := make([]pier.Session, 0, len(all))
	for _, s := range all {
		if !pier.BakeMachine(s) {
			out = append(out, s)
		}
	}
	return out
}

func (p *overviewPoller) quotaDue(ctx context.Context, be Account) {
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
	prs, checked := p.s.prs.snapshot()
	o := buildOverview(be, sessions, quota, enriched, prs, checked)
	p.mu.Lock()
	if !p.have {
		close(p.first)
		defer p.s.prs.kick() // the first listing: its pull requests are due
	}
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

// listed is the latest listing, once there is one.
func (p *overviewPoller) listed() ([]pier.Session, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions, p.have && p.lastErr == nil && p.sessions != nil
}

// republish sends the overview again from the latest listing (its pull
// requests changed).
func (p *overviewPoller) republish(be Backend) {
	p.mu.Lock()
	sessions, enriched := p.sessions, p.last.Enriched
	p.mu.Unlock()
	if sessions != nil {
		p.publish(be, sessions, enriched)
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
	if !p.have {
		close(p.first)
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

// latest is the overview as it is now — or, before there's any, the first
// one: the sessions as listed, which their statuses (read from each VM,
// seconds apiece over a tunnel) follow as an update.
func (p *overviewPoller) latest(ctx context.Context) wire.Overview {
	select {
	case <-p.first:
	case <-ctx.Done():
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
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
	// Without a watch no loop reads pull requests: read them here, when
	// they're as due as they would be for a client looking at them.
	p.mu.Lock()
	looping := p.cancel != nil
	p.mu.Unlock()
	if sessions, ok := p.listed(); ok && !looping {
		if be, err := p.s.backend(); err == nil && p.s.prs.wait(true, be.GitHubLimit()) == 0 && p.s.prs.refresh(ctx, be, sessions) {
			p.republish(be)
		}
	}
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
			unsubscribe := s.overview.subscribe(sub.Emit)
			go func() {
				<-sub.Done()
				unsubscribe()
			}()
			return s.overview.latest(ctx), nil
		})
}

package api

import (
	"context"
	"sync"
	"time"

	"github.com/usepier/pier/internal/driver/payload"
)

// checkoutFetch is how often the linked clones fetch origin's default
// branch while the overview is watched. Sessions, the pool and images are
// made from it, so what the cockpit says of a repo (set up for pier, needs
// an image, its image stale) is of origin's branch, not of how long ago
// this Mac last pulled.
const checkoutFetch = 5 * time.Minute

type checkoutFetches struct {
	mu      sync.Mutex
	at      time.Time
	running bool
}

// fetchCheckouts brings each linked clone's view of origin's default branch
// up to date, in the background and at most every checkoutFetch, and
// refreshes the overview after.
func (s *Server) fetchCheckouts(be Backend) {
	f := &s.fetches
	f.mu.Lock()
	if f.running || time.Since(f.at) < checkoutFetch {
		f.mu.Unlock()
		return
	}
	f.running, f.at = true, time.Now()
	f.mu.Unlock()
	go func() {
		defer func() {
			f.mu.Lock()
			f.running = false
			f.mu.Unlock()
		}()
		for path := range be.Config().Checkouts {
			ctx, cancel := context.WithTimeout(s.root, time.Minute)
			payload.OriginBase(ctx, path)
			cancel()
		}
		if s.root.Err() == nil {
			s.overview.kick(0)
		}
	}()
}

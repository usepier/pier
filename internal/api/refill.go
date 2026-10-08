package api

import (
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
)

// With the refill setting on, pools are kept at their size while the app
// runs: after every enriched refresh, each linked repo whose pool is below
// its size gets a background fill — unless one is running, one started a
// moment ago, or the last one failed recently.

const (
	refillCooldown = 10 * time.Minute // between fills of one repo
	refillBackoff  = time.Hour        // after a failed fill
)

type poolRefill struct {
	mu   sync.Mutex
	next map[string]time.Time // repo → earliest next fill
}

// due lists the repo roots to fill now.
func (r *poolRefill) due(be Backend, sessions []pier.Session, jobs []pier.Job, now time.Time) []string {
	if !be.Config().Speed.AutoRefill {
		return nil
	}
	var roots []string
	for _, co := range be.Checkouts() {
		if !co.Missing {
			roots = append(roots, co.Path)
		}
	}
	busy := map[string]bool{}
	for _, j := range jobs {
		if j.Kind != pier.JobFill {
			continue
		}
		switch {
		case j.State == pier.JobRunning:
			busy[j.Repo] = true
		case j.State == pier.JobFailed && now.Sub(j.Ended) < refillBackoff:
			busy[j.Repo] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		r.next = map[string]time.Time{}
	}
	var out []string
	for _, repo := range be.ReposFor(sessions, roots) {
		if repo.Root == "" || repo.PoolSize == 0 || repo.Ready+repo.Filling >= repo.PoolSize || busy[repo.Name] {
			continue
		}
		if now.Before(r.next[repo.Name]) {
			continue
		}
		r.next[repo.Name] = now.Add(refillCooldown)
		out = append(out, repo.Root)
	}
	return out
}

// refill starts the fills that are due.
func (s *Server) refill(be Backend, sessions []pier.Session) {
	for _, root := range s.pools.due(be, sessions, pier.Jobs(), time.Now()) {
		if _, err := be.StartFill(root); err != nil {
			s.logf("refilling the pool of %s: %v", root, err)
			continue
		}
		s.logf("refilling the pool of %s", root)
	}
}

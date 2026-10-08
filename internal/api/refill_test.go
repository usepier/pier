package api

import (
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

type refillFake struct {
	*fake
	cfg   config.Config
	repos []pier.Repo
}

func (f *refillFake) Config() config.Config { return f.cfg }
func (f *refillFake) Checkouts() []pier.Checkout {
	return []pier.Checkout{{Path: "/code/shop", Name: "shop"}, {Path: "/code/api", Name: "api"}, {Path: "/gone", Name: "gone", Missing: true}}
}
func (f *refillFake) ReposFor([]pier.Session, []string) []pier.Repo { return f.repos }

func TestRefillDue(t *testing.T) {
	cfg := config.Default()
	f := &refillFake{fake: &fake{}, cfg: cfg, repos: []pier.Repo{
		{Name: "shop", Root: "/code/shop", PoolSize: 1},                     // empty: fill
		{Name: "api", Root: "/code/api", PoolSize: 2, Ready: 1, Filling: 1}, // full enough
		{Name: "web", Root: "/code/web", PoolSize: 1},                       // a fill already runs
		{Name: "docs", Root: "/code/docs", PoolSize: 1},                     // its last fill just failed
		{Name: "theirs", PoolSize: 1},                                       // no clone here
		{Name: "lean", Root: "/code/lean", PoolSize: 0},                     // no pool
	}}
	now := time.Now()
	jobs := []pier.Job{
		{Kind: pier.JobFill, Repo: "web", State: pier.JobRunning},
		{Kind: pier.JobFill, Repo: "docs", State: pier.JobFailed, Ended: now.Add(-time.Minute)},
	}
	var r poolRefill
	if got := r.due(f, nil, jobs, now); len(got) != 0 {
		t.Fatalf("refill is opt-in, got %v", got)
	}
	f.cfg.Speed.AutoRefill = true
	if got := r.due(f, nil, jobs, now); len(got) != 1 || got[0] != "/code/shop" {
		t.Fatalf("due = %v", got)
	}
	if got := r.due(f, nil, jobs, now.Add(time.Minute)); len(got) != 0 {
		t.Errorf("a repo just filled waits out the cooldown, got %v", got)
	}
	if got := r.due(f, nil, jobs, now.Add(refillCooldown+time.Second)); len(got) != 1 {
		t.Errorf("after the cooldown it fills again, got %v", got)
	}
}

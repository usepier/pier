package pier

import (
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/pool"
)

func TestSweepable(t *testing.T) {
	cfg := config.Default()
	cfg.Driver = "aws-ec2"
	cfg.RecordBake("pooled", "ami-1") // pool size follows the default for baked repos
	c := &Client{cfg: cfg}
	now := time.Now()
	maxAge := 14 * 24 * time.Hour
	member := func(repo string, state driver.State, age time.Duration) Session {
		return Session{Name: "pool-" + repo, Repo: repo, PoolGen: "g", State: state, Created: now.Add(-age)}
	}
	for _, tc := range []struct {
		name string
		s    Session
		want bool
	}{
		{"claimable member", member("pooled", driver.StateParked, time.Hour), false},
		{"member still filling", member("pooled", driver.StateRunning, time.Hour), false},
		{"repo without a pool", member("unbaked", driver.StateParked, time.Hour), true},
		{"dead member", member("pooled", driver.StateDead, time.Hour), true},
		{"over the recycle age", member("pooled", driver.StateParked, maxAge), true},
		{"fill that never parked", member("pooled", driver.StateRunning, pool.FillGrace), true},
		{"already deleting", member("unbaked", driver.StateDeleting, time.Hour), false},
		{"user session", Session{Name: "feat", Repo: "unbaked", State: driver.StateParked, Created: now.Add(-maxAge)}, false},
		{"bake in progress", Session{Name: "bake", Repo: "bake", State: driver.StateRunning, Created: now.Add(-time.Hour)}, false},
		{"abandoned bake", Session{Name: "bake", Repo: "bake", State: driver.StateRunning, Created: now.Add(-bakeMaxLife)}, true},
	} {
		if got := c.sweepable(tc.s, maxAge, now); got != tc.want {
			t.Errorf("%s: sweepable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

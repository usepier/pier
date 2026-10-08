package pier

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/pool"
)

func TestSweepable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keys := filepath.Join(config.Dir(), "keys")
	os.MkdirAll(keys, 0o700)
	cfg := config.Default()
	cfg.Driver = "aws-ec2"
	cfg.RecordBake("pooled", "ami-1") // pool size follows the default for baked repos
	c := &Client{cfg: cfg}
	now := time.Now()
	maxAge := 14 * 24 * time.Hour
	n := 0
	// mine: made here, so its key is here.
	mine := func(s Session) Session {
		n++
		s.ID = "i-" + strconv.Itoa(n)
		os.WriteFile(filepath.Join(keys, s.ID+".pem"), []byte("k"), 0o600)
		return s
	}
	member := func(repo string, state driver.State, age time.Duration) Session {
		return mine(Session{Name: "pool-" + repo, Repo: repo, PoolGen: "g", State: state, Created: now.Add(-age)})
	}
	elsewhere := func(s Session) Session {
		s.ID = "i-elsewhere"
		return s
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
		{"user session", mine(Session{Name: "feat", Repo: "unbaked", State: driver.StateParked, Created: now.Add(-maxAge)}), false},
		{"bake in progress", mine(Session{Name: "bake", Repo: "bake", State: driver.StateRunning, Created: now.Add(-time.Hour)}), false},
		{"abandoned bake", mine(Session{Name: "bake", Repo: "bake", State: driver.StateRunning, Created: now.Add(-bakeMaxLife)}), true},
		// Another machine's: never this one's to remove, whatever this
		// machine's config says.
		{"another machine's member of a repo without a pool here", elsewhere(Session{Name: "pool-x", Repo: "unbaked", PoolGen: "g", State: driver.StateParked, Created: now.Add(-time.Hour)}), false},
		{"another machine's dead member", elsewhere(Session{Name: "pool-x", Repo: "pooled", PoolGen: "g", State: driver.StateDead, Created: now.Add(-time.Hour)}), false},
		{"another machine's long bake", elsewhere(Session{Name: "bake", Repo: "bake", State: driver.StateRunning, Created: now.Add(-bakeMaxLife)}), false},
	} {
		if got := c.sweepable(tc.s, maxAge, now); got != tc.want {
			t.Errorf("%s: sweepable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

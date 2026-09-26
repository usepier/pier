package pier

import (
	"context"
	"fmt"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/pool"
)

// Doctor runs the cloud driver's environment and account checks, plus
// pier's own: ready sessions nothing will claim, and ones stuck filling.
func (c *Client) Doctor(ctx context.Context) []Check {
	checks := c.drv.Doctor(ctx)
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return checks
	}
	// Orphans are parked money nothing will ever claim: repos whose ready
	// sessions were turned off (or another machine's config). Members
	// unparked past the fill grace are worse — a refill died before its
	// member parked, and it bills at full rate until the next refill
	// reconciles it away.
	orphans, stuck := map[string]int{}, map[string]int{}
	for _, s := range sessions {
		switch {
		case s.PoolGen == "" || s.State == driver.StateDeleting:
		case s.State != driver.StateParked && s.State != driver.StateDead &&
			time.Since(s.Created) >= pool.FillGrace:
			stuck[s.Repo]++
		case c.cfg.PoolSize(s.Repo) == 0:
			orphans[s.Repo]++
		}
	}
	for repo, n := range orphans {
		checks = append(checks, Check{
			Name:   "ready sessions " + repo,
			Detail: fmt.Sprintf("%d ready session(s) but none configured — `pier ready 0` in %s removes them", n, repo),
		})
	}
	for repo, n := range stuck {
		checks = append(checks, Check{
			Name:   "ready sessions " + repo,
			Detail: fmt.Sprintf("%d stuck filling for 2h+ — running at full price; `pier ready 0` in %s removes them", n, repo),
		})
	}
	return checks
}

// Teardown destroys every ready session, then all pier groundwork and
// images in the account, and forgets the images and ready-session settings.
// Live sessions block it: the driver refuses while instances exist.
func (c *Client) Teardown(ctx context.Context, progress Progress) error {
	start := time.Now()
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return err
	}
	repos := map[string]bool{}
	for _, s := range sessions {
		if s.PoolGen != "" {
			repos[s.Repo] = true
		}
	}
	for repo := range repos {
		if _, err := pool.Drain(ctx, c.drv, sessions, repo, progress.stepper(start)); err != nil {
			return err
		}
	}
	if err := c.drv.Teardown(ctx); err != nil {
		return err
	}
	next, err := config.Update(func(cfg *config.Config) error {
		cfg.ClearBakes()
		cfg.Pool.Sizes = nil
		return nil
	})
	if err == nil {
		c.cfg = next
	}
	return err
}

// Settings returns the settings schema with current values: every field
// that applies to the active cloud, in page order.
func (c *Client) Settings() []SettingValue {
	var out []SettingValue
	for _, f := range config.Settings {
		if !c.cfg.Visible(f) {
			continue
		}
		v := config.Get(c.cfg, f.Key)
		out = append(out, SettingValue{Field: f, Value: v, Display: f.Display(v)})
	}
	return out
}

// SettingValue is one settings row: the field's schema and its value.
type SettingValue struct {
	config.Field
	Value   string // stored value
	Display string // human rendering
}

// Set validates and saves one setting. The client's view refreshes, but
// the driver doesn't: build a new Client for changes to take effect on
// cloud calls.
func (c *Client) Set(key, value string) error {
	next, err := config.Update(func(cfg *config.Config) error {
		return config.Set(cfg, key, value)
	})
	if err != nil {
		return err
	}
	c.cfg = next
	return nil
}

// MachineCatalog lists the machine types the settings picker offers for the
// active cloud, around currentType.
func (c *Client) MachineCatalog(currentType string) []Machine {
	return c.drv.Machines(currentType)
}

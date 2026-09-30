package pier

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/pool"
)

// Nothing pier creates may outlive its purpose: not when a config is lost,
// not across an update that changes what a pooled session or an image looks
// like. Everything pier makes is tagged in the cloud, so the cloud — not the
// config — decides what exists, and these sweeps collect whatever pier
// would never use again.

// bakeMaxLife bounds a bake instance: harness install, the bake hook, a
// prebuild and the image wait fit well inside it. One still running past it
// lost its bake process (killed, laptop gone) and has no supervisor to park it.
const bakeMaxLife = 4 * time.Hour

// sweepable reports whether pier would never use s again: a pooled session
// nothing will claim, or a bake instance whose bake is gone.
func (c *Client) sweepable(s Session, maxAge time.Duration, now time.Time) bool {
	if s.State == driver.StateDeleting || s.State == driver.StateFailed {
		return false
	}
	lived := now.Sub(s.Created)
	if s.PoolGen == "" {
		return s.Name == "bake" && s.Repo == "bake" && lived >= bakeMaxLife
	}
	switch {
	case s.State == driver.StateDead,
		// No pool for this repo (size 0, or no session image to pool): a
		// claim never looks, so nothing ever would.
		c.cfg.PoolSize(s.Repo) == 0,
		maxAge > 0 && lived >= maxAge,
		s.State != driver.StateParked && lived >= pool.FillGrace:
		return true
	}
	return false
}

// sweep destroys every sweepable session in the list and returns the list
// with those rows marked deleting. A destroy that fails leaves its row as
// it was; the next listing tries again.
func (c *Client) sweep(ctx context.Context, sessions []Session) []Session {
	maxAge, _ := c.cfg.PoolMaxAge() // unparsable → 0, which skips the age check
	now := time.Now()
	out := append([]Session(nil), sessions...)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var gone []string
	for i := range out {
		if !c.sweepable(out[i], maxAge, now) {
			continue
		}
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			if err := c.drv.Destroy(context.WithoutCancel(ctx), s.ID); err != nil {
				c.notify("could not remove stray " + s.Name + ": " + err.Error())
				return
			}
			s.State = driver.StateDeleting
			mu.Lock()
			gone = append(gone, s.Name)
			mu.Unlock()
		}(&out[i])
	}
	wg.Wait()
	for _, name := range gone {
		c.notify("removed " + name + " — nothing would ever use it")
	}
	return out
}

func (c *Client) notify(msg string) {
	if c.opts.Notify != nil {
		c.opts.Notify(msg)
	}
}

// ImageReport is what ReconcileImages changed.
type ImageReport struct {
	Adopted map[string]string // repo → image now recorded for it
	Deleted []string          // superseded images removed
}

// ReconcileImages makes the config agree with the cloud: each repo uses its
// newest image, older ones are deleted, and references to images that no
// longer exist are dropped. The newest wins even over the config's pick —
// it can only be a bake that finished after the config last heard of it.
func (c *Client) ReconcileImages(ctx context.Context) (ImageReport, error) {
	imgs, err := c.drv.Images(ctx)
	if err != nil {
		return ImageReport{}, err
	}
	rep := ImageReport{Adopted: map[string]string{}}
	newest := map[string]driver.Image{} // by tagged (sanitized) repo name
	var stale []driver.Image
	for _, img := range imgs { // newest first
		if _, ok := newest[img.Repo]; ok {
			stale = append(stale, img)
			continue
		}
		newest[img.Repo] = img
	}
	next, err := config.Update(func(cfg *config.Config) error {
		known := map[string]string{} // tagged name → config key
		for _, repo := range cfg.BakedRepos() {
			known[payload.Sanitize(repo)] = repo
			if _, ok := newest[payload.Sanitize(repo)]; !ok {
				cfg.ForgetBake(repo) // the image is gone: launching from it would fail
			}
		}
		for tagged, img := range newest {
			repo, ok := known[tagged]
			if !ok {
				repo = tagged
			}
			if cfg.BakedImage(repo) == img.ID {
				continue
			}
			// Bake details describe the image they were recorded with.
			cfg.ForgetBake(repo)
			cfg.RecordBake(repo, img.ID)
			rep.Adopted[repo] = img.ID
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	c.cfg = next
	for _, img := range stale {
		if err := c.drv.DeleteImage(ctx, img.ID); err != nil {
			return rep, fmt.Errorf("deleting superseded image %s: %w", img.ID, err)
		}
		rep.Deleted = append(rep.Deleted, img.ID)
	}
	return rep, nil
}

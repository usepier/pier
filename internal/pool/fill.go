package pool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// Fill reconciles one repo's pool to Params.Size: stale members recycle,
// missing ones get created, set up, and parked — one at a time; a fill is
// background work, and serial keeps quota pressure and progress readable.
// A member is created exactly like a session (same Create, same payload,
// same async .pier-setup.sh) under a placeholder name, except its supervisor
// runs on FillLeash — if this process dies, the member still self-parks
// shortly after setup finishes instead of idling at full price.
func Fill(ctx context.Context, p Params) error {
	progress := p.progress()
	sessions, err := p.Driver.List(ctx)
	if err != nil {
		return err
	}
	repo := filepath.Base(p.RepoRoot)
	plan := Reconcile(Members(sessions, repo), p.Size, p.Gen, p.MaxAge, time.Now())
	// These destroys act on a List snapshot seconds old. A claim committing in
	// that window turns a member into a session this loop would still destroy —
	// but claims only take Ready members and this loop only stale ones, so the
	// two disagree about a member only across a re-bake racing a create. Known,
	// accepted: the claimer fails loudly and falls back to a fresh create.
	for _, m := range plan.Stale {
		progress("recycling stale member " + m.Name)
		if err := p.Driver.Destroy(ctx, m.ID); err != nil {
			return err
		}
	}
	if plan.Fill == 0 {
		progress(fmt.Sprintf("pool %s is full (%d ready, %d filling)", repo, len(plan.Ready), plan.InFlight))
		return nil
	}
	// Quota gate, conservative: at least 2 vCPUs per member. Pools must not
	// eat the headroom real sessions need; an unreadable quota is not fatal —
	// the create itself fails loudly on the provider's limit.
	if q, err := p.Driver.Headroom(ctx); err == nil && q.Limit > 0 && q.Limit-q.Used < 2*plan.Fill {
		return fmt.Errorf("filling %d member(s) needs more vCPU headroom than %s has — raise the quota or shrink the pool",
			plan.Fill, q.Detail)
	}
	hasSetup := len(SetupScript(p.RepoRoot)) > 0
	for i := range plan.Fill {
		progress(fmt.Sprintf("filling member %d/%d", i+1, plan.Fill))
		if err := fillOne(ctx, p, hasSetup); err != nil {
			return err
		}
	}
	progress(fmt.Sprintf("pool %s is full (%d ready)", repo, len(plan.Ready)+plan.Fill))
	return nil
}

func fillOne(ctx context.Context, p Params, hasSetup bool) error {
	name, err := placeholderName(p.RepoRoot)
	if err != nil {
		return err
	}
	sess, err := p.Driver.Create(ctx, driver.CreateSpec{
		Name:          name,
		Repo:          p.RepoRoot,
		Branch:        name, // placeholder; claim's freshen replaces it
		Image:         p.Image,
		IdleTimeout:   FillLeash,
		UnattendedCap: p.UnattendedCap,
		PoolGen:       p.Gen,
		Progress:      p.progress(),
	})
	if err != nil {
		return err
	}
	if hasSetup {
		p.progress()("waiting for .pier-setup.sh — a member is only warm once setup finished")
		if err := waitSetup(ctx, p, sess.ID); err != nil {
			// An unfinished setup is exactly what the pool exists to avoid:
			// destroy rather than park a member that would claim cold.
			p.progress()("destroying the failed member")
			_ = p.Driver.Destroy(context.WithoutCancel(ctx), sess.ID)
			return err
		}
	}
	p.progress()("parking " + name)
	return p.Driver.Park(ctx, sess.ID)
}

// waitSetup polls the member's setup status (the same file the supervisor
// beacons to ls/TUI). "running" — and, before the setup window has spun up,
// a missing file — keep it waiting; "0" succeeds; anything else fails with
// the log tail.
func waitSetup(ctx context.Context, p Params, id string) error {
	deadline := time.Now().Add(setupWait)
	for {
		out, err := p.Driver.Exec(ctx, id, "cat ~/.pier-setup.status 2>/dev/null || echo none")
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err == nil {
			switch status := strings.TrimSpace(out); status {
			case "0":
				return nil
			case "none", "running":
			default:
				tail, _ := p.Driver.Exec(ctx, id, "tail -n 15 ~/.pier-setup.log 2>/dev/null")
				return fmt.Errorf(".pier-setup.sh failed (exit %s) on the pool member:\n%s", status, tail)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("setup still running after %s — not pooling this member", setupWait)
		}
		time.Sleep(10 * time.Second)
	}
}

// Drain destroys every pool member of the repo, any generation, and returns
// how many it took down. Claimed members are sessions, not members — they
// are never touched.
func Drain(ctx context.Context, drv driver.Driver, sessions []driver.Session, repo string, progress func(string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	n := 0
	for _, m := range Members(sessions, repo) {
		if m.State == driver.StateDeleting {
			continue
		}
		progress("destroying " + m.Name)
		if err := drv.Destroy(ctx, m.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

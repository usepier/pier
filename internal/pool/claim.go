package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/payload"
)

// Claim tries to turn a warm pool member into the requested session:
// tag-claim it (nonce read-back settles races), resume it, freshen it —
// secrets re-pushed, repo moved to the requested branch/base, dirty patch
// applied, the user's supervisor timeouts restored, setup re-run async to
// catch drift since fill. Returns (nil, nil) when no member is claimable or
// a freshen fails (the member is destroyed: its state is untrusted) — either
// way the caller falls back to a fresh create.
func Claim(ctx context.Context, p Params, sessions []driver.Session, name, branch, baseRef string) (*driver.Session, error) {
	progress := p.progress()
	// The create path validates names inside Create; the claim path writes
	// them straight into tags and the freshen script, so it validates here —
	// before anything touches the provider.
	if err := payload.ValidateNames(driver.CreateSpec{Name: name, Repo: p.RepoRoot, Branch: branch, BaseRef: baseRef}); err != nil {
		return nil, err
	}
	plan := Reconcile(Members(sessions, filepath.Base(p.RepoRoot)), p.Size, p.Gen, p.MaxAge, time.Now())
	if len(plan.Ready) == 0 {
		return nil, nil
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	var member *driver.Session
	for _, i := range pickOrder(len(plan.Ready), nonce) {
		m := plan.Ready[i]
		switch err := p.Driver.Claim(ctx, m.ID, driver.ClaimSpec{Name: name, Branch: branch, Nonce: nonce}); {
		case errors.Is(err, driver.ErrClaimLost):
			continue
		case err != nil:
			// A pool accelerates, never gates: a member destroyed under us, a
			// throttled API, a label CAS conflict — skip the candidate; when
			// none work, the caller creates fresh.
			progress("claim of " + m.Name + " failed (" + err.Error() + ") — skipping")
			continue
		}
		member = &m
		break
	}
	if member == nil {
		return nil, nil
	}
	progress(fmt.Sprintf("claimed warm member %s — resuming", member.Name))
	if err := freshen(ctx, p, member, name, branch, baseRef); err != nil {
		progress("freshen failed (" + err.Error() + ")")
		progress("destroying the member — its state is untrusted — and creating fresh")
		if derr := p.Driver.Destroy(context.WithoutCancel(ctx), member.ID); derr != nil {
			progress("destroy failed too (" + derr.Error() + ") — check `pier pool` for a leftover member")
		}
		return nil, nil
	}
	return &driver.Session{
		ID: member.ID, Name: name, Repo: filepath.Base(p.RepoRoot), Branch: branch,
		User: member.User, Driver: p.Driver.Name(), State: driver.StateRunning,
		Created: time.Now(), InstanceType: member.InstanceType,
	}, nil
}

func freshen(ctx context.Context, p Params, member *driver.Session, name, branch, baseRef string) error {
	progress := p.progress()
	// Replacing FillLeash with the user's timeouts is the first act on the
	// live member — inside the resume goroutine, so a slow payload build
	// can't hand the still-leashed supervisor two idle minutes to park the
	// member back mid-claim.
	unleash := fmt.Sprintf(
		"sudo sed -i 's/^idle_timeout=.*/idle_timeout=%s/; s/^unattended_cap=.*/unattended_cap=%s/' /etc/pier/supervisor.conf",
		payload.DurConf(p.IdleTimeout), payload.DurConf(p.UnattendedCap))
	// Resume and local payload prep overlap, the same trick create plays with
	// the boot: claim latency is the whole point of the pool.
	resumed := make(chan error, 1)
	go func() {
		if err := p.Driver.Resume(ctx, member.ID); err != nil {
			resumed <- err
			return
		}
		_, err := p.Driver.Exec(ctx, member.ID, unleash)
		resumed <- err
	}()

	work, err := os.MkdirTemp("", "pier-claim-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	spec := driver.CreateSpec{
		Name: name, Repo: p.RepoRoot, Branch: branch, BaseRef: baseRef,
		IdleTimeout: p.IdleTimeout, UnattendedCap: p.UnattendedCap,
	}
	pl, plErr := payload.BuildFreshen(ctx, work, spec, member.Name, p.Manifest, p.SessionEnv, progress)
	if err := <-resumed; err != nil {
		return err
	}
	if plErr != nil {
		return plErr
	}

	// SSHTarget is the driver contract for features that manage their own
	// ssh/scp processes — the same seam pier proxy rides.
	opts, dest, err := p.Driver.SSHTarget(ctx, member.ID)
	if err != nil {
		return err
	}
	for _, n := range pl.Notes {
		progress(n)
	}
	for _, push := range pl.Pushes {
		if err := scpTo(ctx, opts, dest, push.Local, push.Remote); err != nil {
			if ctx.Err() != nil {
				return err
			}
			// One retry: tunnels drop right as a just-started instance
			// settles, exactly as during create.
			progress("push interrupted — retrying")
			time.Sleep(2 * time.Second)
			if err := scpTo(ctx, opts, dest, push.Local, push.Remote); err != nil {
				return err
			}
		}
	}
	progress("freshening: branch, secrets, setup re-run")
	var extra []string
	if pl.ForwardAgent {
		extra = []string{"-A"}
	}
	if out, err := sshRun(ctx, opts, extra, dest, "bash /tmp/pier-freshen.sh"); err != nil {
		return fmt.Errorf("freshen: %w\n%s", err, out)
	}
	return nil
}

func scpTo(ctx context.Context, opts []string, dest, local, remote string) error {
	args := slices.Concat(opts, []string{local, dest + ":" + remote})
	cmd := exec.CommandContext(ctx, "scp", args...)
	// scp draws its progress meter only when stdout is a terminal, same as
	// the drivers' pushes.
	cmd.Stdout = os.Stdout
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp %s -> %s: %s", local, dest, strings.TrimSpace(errb.String()))
	}
	return nil
}

func sshRun(ctx context.Context, opts, extra []string, dest, script string) (string, error) {
	args := slices.Concat(opts, extra, []string{dest, script})
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh %s: %s", dest, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

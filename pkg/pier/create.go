package pier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/pool"
	"github.com/usepier/pier/internal/tombstone"
)

// CreateRequest describes a new session.
type CreateRequest struct {
	RepoRoot string // local checkout the session is made from
	Branch   string // new branch (and session name)
	Base     string // base ref; "" = HEAD
	// Idle and Cap override the configured park timeouts for this session
	// (nil = config; 0 = never).
	Idle, Cap *time.Duration
	// NoReady skips claiming one of the repo's ready sessions.
	NoReady  bool
	Progress Progress
}

// CreateResult is a finished create.
type CreateResult struct {
	Session Session
	Claimed bool // came from a ready session rather than a fresh launch
	// RefillLog is set when a background refill of the repo's ready
	// sessions started; RefillErr when one should have but couldn't.
	RefillLog string
	RefillErr error
}

// Create makes a session: it claims one of the repo's ready sessions when
// there is one (resume, branch, secrets, setup re-run — seconds), and
// otherwise launches fresh from the repo's image. Either way the repo's
// ready sessions refill in the background. A create that fails leaves a
// failed row behind (its instance is already gone), so it never vanishes
// without trace.
func (c *Client) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	start := time.Now()
	step := req.Progress.stepper(start)
	base := req.Base
	if base == "" {
		base = "HEAD"
	}
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	for _, s := range sessions {
		if s.PoolGen == "" && s.Name == req.Branch {
			return CreateResult{}, fmt.Errorf("session %q already exists — `pier attach %s`", req.Branch, req.Branch)
		}
	}
	idle, err := c.duration(req.Idle, c.cfg.IdleTimeout)
	if err != nil {
		return CreateResult{}, fmt.Errorf("idle timeout: %w", err)
	}
	cap_, err := c.duration(req.Cap, c.cfg.UnattendedCap)
	if err != nil {
		return CreateResult{}, fmt.Errorf("runaway cap: %w", err)
	}
	repo := filepath.Base(req.RepoRoot)
	image := c.cfg.BakedImage(repo)
	// A repo with a bake hook has declared that stock isn't enough for it.
	// Launching stock anyway is legal but almost never what was wanted: the
	// create succeeds, then .pier/setup.sh dies minutes later on a missing
	// toolchain. Say it now, while it's one sentence.
	if image == "" && driver.BakeHook(req.RepoRoot) != "" {
		req.Progress.emit(start, EventWarn, "no session image for "+repo+
			" — .pier/bake.sh toolchains will be missing and .pier/setup.sh may fail; `pier bake` fixes it")
	}

	poolable := c.cfg.PoolSize(repo) > 0 && !req.NoReady
	var res CreateResult
	if poolable {
		pp, err := c.poolParams(req.RepoRoot, req.Progress, start)
		if err != nil {
			return CreateResult{}, err
		}
		pp.IdleTimeout, pp.UnattendedCap = idle, cap_
		sess, err := pool.Claim(ctx, pp, sessions, req.Branch, req.Branch, base)
		if err != nil {
			// A ready session accelerates, never gates: whatever broke the
			// claim, the fresh create below gives the same answer or a session.
			req.Progress.emit(start, EventWarn, "claim failed: "+err.Error())
		}
		if sess != nil {
			res.Session, res.Claimed = *sess, true
		} else {
			step("no ready session to claim — creating fresh")
		}
	}
	if !res.Claimed {
		sess, err := c.drv.Create(ctx, driver.CreateSpec{
			Name: req.Branch, Repo: req.RepoRoot, Branch: req.Branch, BaseRef: base, Image: image,
			IdleTimeout: idle, UnattendedCap: cap_,
			Progress: step,
		})
		if err != nil {
			c.bury(req, err)
			return CreateResult{}, err
		}
		res.Session = *sess
	}
	if poolable {
		// Top the ready sessions back up — after a claim (one was consumed)
		// and after a fallback create (there were too few) alike.
		res.RefillLog, res.RefillErr = c.SpawnRefill(req.RepoRoot)
	}
	return res, nil
}

func (c *Client) duration(override *time.Duration, configured string) (time.Duration, error) {
	if override != nil {
		return *override, nil
	}
	return config.ParkDuration(configured)
}

// CreateLogPath is where a detached create's output lands. SpawnCreate
// writes it and a failed create points its record at it. It shares the
// record store's escaping, so feat/login and feat-login get their own logs.
func CreateLogPath(branch string) string {
	return filepath.Join(config.Dir(), "logs", "create-"+tombstone.FileStem(branch)+".log")
}

// SpawnCreate runs a create for branch in the background (a detached
// `pier <branch> --detach` working in repoRoot) and returns its log path.
// It survives the frontend closing; the session lists as creating until
// it's ready.
func (c *Client) SpawnCreate(repoRoot, branch string) (string, error) {
	if repoRoot == "" {
		return "", ErrNotInRepo
	}
	logPath := CreateLogPath(branch)
	return logPath, spawnDetached(repoRoot, logPath, false, branch, "--detach")
}

// bury records a failed create. Best-effort: the create already failed and
// its own error is what the user acts on — a record write that fails must
// not mask it.
func (c *Client) bury(req CreateRequest, cause error) {
	drv := c.cfg.Driver
	if drv == "" {
		drv = "aws-ec2"
	}
	rec := tombstone.Record{
		Name: req.Branch, Repo: filepath.Base(req.RepoRoot), Branch: req.Branch,
		Driver: drv, Reason: cause.Error(), When: time.Now(),
	}
	if p := CreateLogPath(req.Branch); fileExists(p) {
		rec.LogPath = p
	}
	_ = tombstone.Write(config.Dir(), rec)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

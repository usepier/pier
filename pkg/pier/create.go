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
	// NoPool skips claiming one of the repo's pooled sessions.
	NoPool   bool
	Progress Progress
}

// CreateResult is a finished create.
type CreateResult struct {
	Session Session
	Claimed bool // came from a pooled session rather than a fresh launch
	// FillLog is set when a background refill of the repo's ready
	// sessions started; FillErr when one should have but couldn't.
	FillLog string
	FillErr error
}

// Create makes a session: it claims one of the repo's pooled sessions when
// there is one (resume, branch, secrets, setup re-run — seconds), and
// otherwise launches fresh from the repo's image. Either way the repo's
// pooled sessions refill in the background. A create that fails leaves a
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
	if c.cfg.BakedImage(repo) == "" {
		// The config may just have lost track of an image the cloud still
		// holds (a reset config, another laptop's bake).
		if _, err := c.ReconcileImages(ctx); err != nil {
			req.Progress.emit(start, EventWarn, "could not look up session images: "+err.Error())
		}
	}
	if err := c.needImage(req.RepoRoot); err != nil {
		return CreateResult{}, err
	}
	image := c.cfg.BakedImage(repo)

	poolable := c.cfg.PoolSize(repo) > 0 && !req.NoPool
	var res CreateResult
	if poolable {
		pp, err := c.poolParams(req.RepoRoot, req.Progress, start)
		if err != nil {
			return CreateResult{}, err
		}
		pp.IdleTimeout, pp.UnattendedCap = idle, cap_
		sess, err := pool.Claim(ctx, pp, sessions, req.Branch, req.Branch, base)
		if err != nil {
			// A pooled session accelerates, never gates: whatever broke the
			// claim, the fresh create below gives the same answer or a session.
			req.Progress.emit(start, EventWarn, "claim failed: "+err.Error())
		}
		if sess != nil {
			res.Session, res.Claimed = *sess, true
		} else {
			step("nothing in the pool to claim — creating fresh")
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
		// Top the pooled sessions back up — after a claim (one was consumed)
		// and after a fallback create (there were too few) alike.
		res.FillLog, res.FillErr = c.SpawnFill(req.RepoRoot)
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

// needImage refuses a repo with a bake hook but no session image. The hook
// declares that the stock image isn't enough: a session launched from it
// runs .pier/setup.sh without the toolchains and fails minutes later.
func (c *Client) needImage(repoRoot string) error {
	repo := filepath.Base(repoRoot)
	if c.cfg.BakedImage(repo) == "" && driver.BakeHook(repoRoot) != "" {
		if BakeRunning(repo) {
			return fmt.Errorf("%s's first session image is still being baked or saved — sessions can start once it's done (log: %s)",
				repo, BakeFinishLogPath(repo))
		}
		return fmt.Errorf("%s has no session image, and its .pier/bake.sh toolchains are needed for sessions to work — run `pier bake` first", repo)
	}
	return nil
}

package pier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/pool"
)

// Repo is one repo's row on the Repos page: its session image, its ready
// sessions, what they cost while waiting, and any rebake reminders.
type Repo struct {
	Name    string
	Current bool   // the repo this process runs from (editable, reminders computed)
	Image   string // session image id; "" = none yet
	// Baked is what the image was baked from; nil for images baked before
	// pier recorded it (or no image).
	Baked *config.ImageInfo
	// ReadyTarget is how many ready sessions pier keeps; Ready are parked and
	// claimable, Filling are being set up, Stale will recycle.
	ReadyTarget, Ready, Filling, Stale int
	ReadyAges                          []string
	Sessions                           int     // live sessions of this repo
	MonthlyUSD                         float64 // idle cost estimate: ready disks + image storage
	Reminders                          []Reminder
}

// Reminder is one piece of rebake guidance. pier never acts on it.
type Reminder struct {
	Message string `json:"message"`
	Action  string `json:"action"` // the command that addresses it
}

// Repos returns every repo pier knows about — baked, with ready sessions or
// ready-session settings, with live sessions, or the one this process runs
// from — sorted by name. sessions is a Sessions() result, so the page and
// the census never disagree.
func (c *Client) Repos(sessions []Session, curRoot string) []Repo {
	names := map[string]bool{}
	for _, r := range c.cfg.PoolRepos() {
		names[r] = true
	}
	for r := range c.cfg.Pool.Sizes {
		names[r] = true
	}
	for r := range c.cfg.Images {
		names[r] = true
	}
	for _, s := range sessions {
		if s.Repo != "" {
			names[s.Repo] = true
		}
	}
	cur := ""
	if curRoot != "" {
		cur = filepath.Base(curRoot)
		names[cur] = true
	}
	curGen := ""
	if cur != "" {
		curGen = c.poolGen(curRoot)
	}
	maxAge, _ := c.cfg.PoolMaxAge() // unparsable → 0, which skips the age check
	now := time.Now()
	var out []Repo
	for name := range names {
		st := FoldReady(sessions, name, cur, curGen, maxAge, now)
		r := Repo{
			Name: name, Current: name == cur,
			Image:       c.cfg.BakedImage(name),
			ReadyTarget: c.cfg.PoolSize(name),
			Ready:       st.Ready, Filling: st.Filling, Stale: st.Stale, ReadyAges: st.Ages,
		}
		if info, ok := c.cfg.Images[name]; ok && r.Image != "" {
			r.Baked = &info
		}
		for _, s := range sessions {
			if s.Repo == name && s.PoolGen == "" && s.State != driver.StateFailed {
				r.Sessions++
			}
		}
		r.MonthlyUSD = float64(st.Ready+st.Filling)*c.DiskMonthlyUSD() + imageMonthlyUSD(r.Image)
		root := ""
		if r.Current {
			root = curRoot
		}
		r.Reminders = c.reminders(name, root)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DiskMonthlyUSD estimates what one parked session's disk costs a month
// (gp3 / pd-balanced list prices, rounded up to cover the priciest regions).
func (c *Client) DiskMonthlyUSD() float64 {
	perGiB := 0.095
	if c.cfg.Driver == "gcp-gce" {
		perGiB = 0.10
	}
	return float64(c.DiskGiB()) * perGiB
}

// imageMonthlyUSD is a flat estimate for one image's snapshot storage: the
// real figure depends on how much of the disk the bake filled.
func imageMonthlyUSD(image string) float64 {
	if image == "" {
		return 0
	}
	return 2
}

// ReadyStats is one repo's ready-session census.
type ReadyStats struct {
	Ready, Filling, Stale int
	Ages                  []string // ready sessions' ages, as listed
}

// FoldReady folds one repo's unclaimed ready sessions out of a session list,
// judging them the way the pool's reconcile would: dead, wrong-generation,
// over the recycle age, or unparked past the fill grace all count stale — a
// member whose fill died hours ago must not read "filling" forever. curGen
// gates the generation check: only the repo the process runs from can
// compute its current generation. maxAge 0 skips the age check.
func FoldReady(sessions []Session, repo, curRepo, curGen string, maxAge time.Duration, now time.Time) ReadyStats {
	var st ReadyStats
	for _, s := range sessions {
		if s.PoolGen == "" || s.Repo != repo {
			continue
		}
		lived := now.Sub(s.Created)
		switch {
		case s.State == driver.StateDeleting:
		case s.State == driver.StateDead,
			repo == curRepo && s.PoolGen != curGen,
			maxAge > 0 && lived >= maxAge:
			st.Stale++
		case s.State == driver.StateParked:
			st.Ready++
			st.Ages = append(st.Ages, Age(s.Created, now))
		case lived >= pool.FillGrace:
			st.Stale++
		default:
			st.Filling++
		}
	}
	return st
}

// Age renders a compact age: now, 5m, 3h, 2d.
func Age(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Reminders is the rebake guidance for the repo at repoRoot. Facts only —
// no image yet, an old image, .pier scripts edited since the bake — and
// nothing at all when bake reminders are off.
func (c *Client) Reminders(repoRoot string) []Reminder {
	return c.reminders(filepath.Base(repoRoot), repoRoot)
}

func (c *Client) reminders(repo, root string) []Reminder {
	if !c.cfg.Speed.BakeReminders {
		return nil
	}
	const bake = "pier bake"
	image := c.cfg.BakedImage(repo)
	if image == "" {
		// Only the repo we stand in: a stranger's repo row without an image
		// is not something this machine can bake.
		if root == "" {
			return nil
		}
		return []Reminder{{
			Message: "No session image for " + repo + " yet. A bake gets new sessions going in a minute or two instead of running the full setup.",
			Action:  bake,
		}}
	}
	info, ok := c.cfg.Images[repo]
	if !ok {
		return nil // baked before pier recorded bake details: nothing honest to say
	}
	var out []Reminder
	if root != "" {
		if info.BakeSHA != fileSHA(filepath.Join(root, ".pier", "bake.sh")) {
			out = append(out, Reminder{
				Message: ".pier/bake.sh changed since " + repo + "'s last bake. New sessions won't have its toolchains until you rebake.",
				Action:  bake,
			})
		}
		if info.SetupSHA != fileSHA(filepath.Join(root, ".pier", "setup.sh")) {
			out = append(out, Reminder{
				Message: ".pier/setup.sh changed since " + repo + "'s last bake. Rebake so new sessions don't redo that work.",
				Action:  bake,
			})
		}
	}
	if maxAge := c.cfg.ReminderAge(); maxAge > 0 && time.Since(info.BakedAt) >= maxAge {
		out = append(out, Reminder{
			Message: fmt.Sprintf("%s's session image is %d days old. Rebake so new sessions start with current dependencies.",
				repo, int(time.Since(info.BakedAt).Hours()/24)),
			Action: bake,
		})
	}
	return out
}

// fileSHA is the hex sha256 of a file, "" when it doesn't exist.
func fileSHA(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BakeRequest describes one `pier bake`.
type BakeRequest struct {
	RepoRoot string
	// IncludeRepo overrides the speed.image_repo setting for this bake:
	// true prebuilds the checkout into the image, false bakes toolchains only.
	IncludeRepo *bool
	Progress    Progress
}

// BakeResult is a finished bake.
type BakeResult struct {
	Image        string
	RepoIncluded bool
	// ReadyStale is set when the repo's ready sessions were built on the
	// previous image; they recycle on the next claim or refill.
	ReadyStale bool
}

// Bake builds (or rebuilds) the repo's session image and records it.
func (c *Client) Bake(ctx context.Context, req BakeRequest) (BakeResult, error) {
	start := time.Now()
	name := filepath.Base(req.RepoRoot)
	include := c.cfg.Speed.ImageRepo
	if req.IncludeRepo != nil {
		include = *req.IncludeRepo
	}
	spec := driver.BakeSpec{
		RepoName: name,
		HookPath: driver.BakeHook(req.RepoRoot),
		// This bake supersedes the repo's previous image (and on aws-ec2,
		// once per config, the legacy shared one).
		Replaces: c.cfg.BakedReplaces(name),
		Progress: req.Progress.stepper(start),
	}
	if include {
		spec.RepoRoot = req.RepoRoot
	}
	img, err := c.drv.Bake(ctx, spec)
	if err != nil {
		return BakeResult{}, err
	}
	info := config.ImageInfo{
		BakedAt:      time.Now().UTC(),
		SetupSHA:     fileSHA(filepath.Join(req.RepoRoot, ".pier", "setup.sh")),
		BakeSHA:      fileSHA(filepath.Join(req.RepoRoot, ".pier", "bake.sh")),
		RepoIncluded: include,
	}
	// Update, not Save: a bake takes minutes, and c.cfg was read before it
	// started. Saving it wholesale would revert anything written meanwhile —
	// including another repo's bake.
	next, err := config.Update(func(cfg *config.Config) error {
		cfg.RecordBake(name, img)
		cfg.RecordImageInfo(name, info)
		return nil
	})
	if err != nil {
		return BakeResult{}, err
	}
	c.cfg = next
	return BakeResult{Image: img, RepoIncluded: include, ReadyStale: next.PoolSize(name) > 0}, nil
}

// poolParams assembles everything internal/pool needs, including the
// generation fingerprint — which is why it must run from inside the repo
// (the local setup script feeds the hash).
func (c *Client) poolParams(repoRoot string, progress Progress, start time.Time) (pool.Params, error) {
	maxAge, err := c.cfg.PoolMaxAge()
	if err != nil {
		return pool.Params{}, err
	}
	idle, err := config.ParkDuration(c.cfg.IdleTimeout)
	if err != nil {
		return pool.Params{}, err
	}
	cap_, err := config.ParkDuration(c.cfg.UnattendedCap)
	if err != nil {
		return pool.Params{}, err
	}
	repo := filepath.Base(repoRoot)
	image := c.cfg.BakedImage(repo)
	return pool.Params{
		Driver:      c.drv,
		RepoRoot:    repoRoot,
		Gen:         pool.Generation(c.drv.Name(), image, c.MachineType(), c.DiskGiB(), pool.SetupScript(repoRoot)),
		Size:        c.cfg.PoolSize(repo),
		MaxAge:      maxAge,
		Image:       image,
		IdleTimeout: idle, UnattendedCap: cap_,
		Manifest: c.cfg.Secrets.Manifest, SessionEnv: SessionEnv(c.cfg),
		Progress: progress.stepper(start),
		Out:      c.opts.Out,
	}, nil
}

func (c *Client) poolGen(repoRoot string) string {
	pp, err := c.poolParams(repoRoot, nil, time.Now())
	if err != nil {
		return ""
	}
	return pp.Gen
}

// SetReady saves how many ready sessions to keep for the repo at repoRoot,
// then reconciles: 0 destroys its ready sessions now, more starts a
// background refill. Returns the refill log path ("" for 0).
func (c *Client) SetReady(ctx context.Context, repoRoot string, n int, progress Progress) (string, error) {
	repo := filepath.Base(repoRoot)
	next, err := config.Update(func(cfg *config.Config) error {
		cfg.SetPoolSize(repo, n)
		return nil
	})
	if err != nil {
		return "", err
	}
	c.cfg = next
	if n == 0 {
		_, err := c.DrainReady(ctx, repo, progress)
		return "", err
	}
	return c.SpawnRefill(repoRoot)
}

// DrainReady destroys a repo's unclaimed ready sessions (any repo; no local
// checkout needed) and returns how many went down.
func (c *Client) DrainReady(ctx context.Context, repo string, progress Progress) (int, error) {
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return 0, err
	}
	return pool.Drain(ctx, c.drv, sessions, repo, progress.stepper(time.Now()))
}

// Refill tops the repo's ready sessions up to target, in the foreground.
func (c *Client) Refill(ctx context.Context, repoRoot string, progress Progress) error {
	pp, err := c.poolParams(repoRoot, progress, time.Now())
	if err != nil {
		return err
	}
	return pool.Fill(ctx, pp)
}

// RefillLogPath is where a background refill of repo writes.
func RefillLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "ready-"+repo+".log")
}

// SpawnRefill re-execs the binary's hidden refill command as a detached
// child working in repoRoot, so a refill survives the frontend exiting.
func (c *Client) SpawnRefill(repoRoot string) (string, error) {
	logPath := RefillLogPath(filepath.Base(repoRoot))
	// Append, never truncate: a claim can spawn a refill while the previous
	// one is still writing here, and clobbering a live writer's log turns
	// both runs into confetti. Refills are rare enough that growth is noise.
	return logPath, spawnDetached(repoRoot, logPath, true, RefillCommand)
}

// RefillCommand is the hidden subcommand SpawnRefill re-execs.
const RefillCommand = "__refill"

// BakeLogPath is where a background bake of repo writes.
func BakeLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".log")
}

// SpawnBake runs `pier bake` for the repo at repoRoot in the background and
// returns its log path. BakeRunning reports on it while it runs.
func (c *Client) SpawnBake(repoRoot string) (string, error) {
	repo := filepath.Base(repoRoot)
	if BakeRunning(repo) {
		return BakeLogPath(repo), fmt.Errorf("a bake of %s is already running", repo)
	}
	logPath := BakeLogPath(repo)
	pid, err := spawnDetachedPID(repoRoot, logPath, false, "bake")
	if err != nil {
		return "", err
	}
	_ = os.WriteFile(bakePIDPath(repo), []byte(strconv.Itoa(pid)), 0o600)
	return logPath, nil
}

// BakeRunning reports whether a background bake of repo started from this
// machine is still running (its process is alive).
func BakeRunning(repo string) bool {
	b, err := os.ReadFile(bakePIDPath(repo))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func bakePIDPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".pid")
}

// spawnDetached starts the pier binary with args in its own session,
// output to logPath, working in dir.
func spawnDetached(dir, logPath string, appendLog bool, args ...string) error {
	_, err := spawnDetachedPID(dir, logPath, appendLog, args...)
	return err
}

func spawnDetachedPID(dir, logPath string, appendLog bool, args ...string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendLog {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(logPath, flags, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go cmd.Wait() // reap if this process outlives the child
	return cmd.Process.Pid, nil
}

var hourlyRe = regexp.MustCompile(`\$([0-9]+(?:\.[0-9]+)?)/h`)

// HourlyUSD reads the hourly rate out of a session's cost note ("~$0.04/h");
// 0 for parked or unknown.
func HourlyUSD(s Session) float64 {
	m := hourlyRe.FindStringSubmatch(s.CostNote)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f
}

package pier

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/pool"
)

// Repo is one repo's row on the Repos page: its session image, its ready
// sessions, what they cost while waiting, and any rebake reminders.
type Repo struct {
	Name    string
	Current bool   // the repo this process runs from (editable, reminders computed)
	Root    string // the local checkout reminders and pool staleness were computed from; "" = none
	Image   string // session image id; "" = none yet
	// Baked is what the image was baked from; nil for images baked before
	// pier recorded it (or no image).
	Baked *config.ImageInfo
	// PoolSize is how many pooled sessions pier keeps; Ready are parked and
	// claimable, Filling are being set up, Stale will recycle.
	PoolSize, Ready, Filling, Stale int
	PoolAges                        []string
	Sessions                        int     // live sessions of this repo
	MonthlyUSD                      float64 // idle cost estimate: ready disks + image storage
	ImageMonthlyUSD                 float64 // the image's share of it
	// PoolFailure is the last fill, when it failed (nil once one succeeds).
	PoolFailure *PoolFailure
	Reminders   []Reminder
}

// Reminder is one piece of rebake guidance. pier never acts on it.
type Reminder struct {
	Message string `json:"message"`
	Action  string `json:"action"` // the command that addresses it
}

// Repos returns every repo pier knows about — baked, with pooled sessions or
// pool settings, with live sessions, or the one this process runs
// from — sorted by name. sessions is a Sessions() result, so the page and
// the census never disagree.
func (c *Client) Repos(sessions []Session, curRoot string) []Repo {
	var roots []string
	if curRoot != "" {
		roots = []string{curRoot}
	}
	out := c.ReposFor(sessions, roots)
	for i := range out {
		out[i].Current = curRoot != "" && out[i].Root == curRoot
	}
	return out
}

// ReposFor is Repos for a frontend with several local checkouts (the Mac
// app's linked ones) instead of one working directory: each root's repo is
// listed, with its reminders and pool staleness computed against that
// checkout. The first root wins when two share a name.
func (c *Client) ReposFor(sessions []Session, roots []string) []Repo {
	names := map[string]bool{}
	for _, r := range c.conf().PoolRepos() {
		names[r] = true
	}
	for r := range c.conf().Pool.Sizes {
		names[r] = true
	}
	for r := range c.conf().Images {
		names[r] = true
	}
	for _, s := range sessions {
		if s.Repo != "" && !BakeMachine(s) {
			names[s.Repo] = true
		}
	}
	rootOf := map[string]string{}
	for _, root := range roots {
		name := filepath.Base(root)
		names[name] = true
		if _, ok := rootOf[name]; !ok {
			rootOf[name] = root
		}
	}
	maxAge, _ := c.conf().PoolMaxAge() // unparsable → 0, which skips the age check
	now := time.Now()
	// The pool this machine can claim from is the members it made.
	var own []Session
	for _, s := range sessions {
		if s.PoolGen == "" || c.HasKey(s) {
			own = append(own, s)
		}
	}
	var out []Repo
	for name := range names {
		root := rootOf[name]
		cur, gen := "", ""
		if root != "" {
			cur, gen = name, c.poolGen(root)
		}
		st := FoldPool(own, name, cur, gen, maxAge, now)
		r := Repo{
			Name: name, Root: root,
			Image:    c.conf().BakedImage(name),
			PoolSize: c.conf().PoolSize(name),
			Ready:    st.Ready, Filling: st.Filling, Stale: st.Stale, PoolAges: st.Ages,
		}
		if info, ok := c.conf().Images[name]; ok && r.Image != "" {
			r.Baked = &info
		}
		for _, s := range sessions {
			if s.Repo == name && s.PoolGen == "" && s.State != driver.StateFailed {
				r.Sessions++
			}
		}
		r.ImageMonthlyUSD = imageMonthlyUSD(r.Image)
		r.MonthlyUSD = float64(st.Ready+st.Filling)*c.DiskMonthlyUSD() + r.ImageMonthlyUSD
		r.PoolFailure = LastPoolFailure(name)
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
	if c.conf().Driver == "gcp-gce" {
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

// PoolStats is one repo's pool census.
type PoolStats struct {
	Ready, Filling, Stale int
	Ages                  []string // pooled sessions' ages, as listed
}

// FoldPool folds one repo's unclaimed pooled sessions out of a session list,
// judging them the way the pool's reconcile would: dead, wrong-generation,
// over the recycle age, or unparked past the fill grace all count stale — a
// member whose fill died hours ago must not read "filling" forever. curGen
// gates the generation check: only the repo the process runs from can
// compute its current generation. maxAge 0 skips the age check.
func FoldPool(sessions []Session, repo, curRepo, curGen string, maxAge time.Duration, now time.Time) PoolStats {
	var st PoolStats
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
	if !c.conf().Speed.BakeReminders {
		return nil
	}
	const bake = "pier bake"
	image := c.conf().BakedImage(repo)
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
	info, ok := c.conf().Images[repo]
	if !ok {
		return nil // baked before pier recorded bake details: nothing honest to say
	}
	var out []Reminder
	if root != "" {
		base := payload.KnownBase(root)
		if info.BakeSHA != bytesSHA(payload.FileAt(root, base, ".pier/bake.sh")) {
			out = append(out, Reminder{
				Message: ".pier/bake.sh changed since " + repo + "'s last bake. New sessions won't have its toolchains until you rebake.",
				Action:  bake,
			})
		}
		if info.SetupSHA != bytesSHA(payload.FileAt(root, base, ".pier/setup.sh")) {
			out = append(out, Reminder{
				Message: ".pier/setup.sh changed since " + repo + "'s last bake. Rebake so new sessions don't redo that work.",
				Action:  bake,
			})
		}
	}
	if maxAge := c.conf().ReminderAge(); maxAge > 0 && time.Since(info.BakedAt) >= maxAge {
		out = append(out, Reminder{
			Message: fmt.Sprintf("%s's session image is %d days old. Rebake so new sessions start with current dependencies.",
				repo, int(time.Since(info.BakedAt).Hours()/24)),
			Action: bake,
		})
	}
	return out
}

// fileSHA is the hex sha256 of a file, "" when it doesn't exist.
// bytesSHA fingerprints a file's content; "" for none.
func bytesSHA(b []byte) string {
	if b == nil {
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
	// Events is the job's event file when the bake runs as one: its
	// background finish carries the job on to the image's end.
	Events string
}

// BakeResult is a bake whose image is being saved.
type BakeResult struct {
	Image        string
	RepoIncluded bool
	// FinishLog is where the background finish writes: it waits for the
	// image, records it, deletes the images it replaces and fills the pool.
	FinishLog string
	// HandedOff says the finish carries the bake's job on (Events): the
	// bake's own process must not end it.
	HandedOff bool
}

// pendingBake is a bake handed to its background finish.
type pendingBake struct {
	Baked driver.Baked
	Info  config.ImageInfo
}

func pendingBakePath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".pending.json")
}

// Bake builds (or rebuilds) the repo's session image. It returns once the
// image is being saved — copying the bake disk into image storage can take
// longer than the bake — and leaves the rest to a background finish.
func (c *Client) Bake(ctx context.Context, req BakeRequest) (BakeResult, error) {
	start := time.Now()
	name := filepath.Base(req.RepoRoot)
	if pid := bakePID(name); pid != 0 && pid != os.Getpid() {
		return BakeResult{}, fmt.Errorf("a bake of %s is already running — log: %s", name, BakeLogPath(name))
	}
	include := c.conf().Speed.ImageRepo
	if req.IncludeRepo != nil {
		include = *req.IncludeRepo
	}
	// The image is made from what sessions are made from: origin's default
	// branch as origin has it now, not this clone (which may be behind).
	base := payload.OriginBase(ctx, req.RepoRoot)
	req.Progress.emit(start, EventStep, "baking from "+base)
	hook := payload.BakeHook(req.RepoRoot, base)
	spec := driver.BakeSpec{
		RepoName: name,
		BaseRef:  base,
		Progress: req.Progress.stepper(start),
	}
	if hook != nil {
		f, err := os.CreateTemp("", "pier-bake-*.sh")
		if err != nil {
			return BakeResult{}, err
		}
		defer os.Remove(f.Name())
		if _, err := f.Write(hook); err != nil {
			f.Close()
			return BakeResult{}, err
		}
		f.Close()
		spec.HookPath = f.Name()
	}
	if include {
		spec.RepoRoot = req.RepoRoot
	}
	baked, err := c.drv.Bake(ctx, spec)
	if err != nil {
		return BakeResult{}, err
	}
	pending, err := json.Marshal(pendingBake{Baked: baked, Info: config.ImageInfo{
		BakedAt:      time.Now().UTC(),
		SetupSHA:     bytesSHA(payload.FileAt(req.RepoRoot, base, ".pier/setup.sh")),
		BakeSHA:      bytesSHA(hook),
		RepoIncluded: include,
	}})
	if err != nil {
		return BakeResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(pendingBakePath(name)), 0o700); err != nil {
		return BakeResult{}, err
	}
	if err := os.WriteFile(pendingBakePath(name), pending, 0o600); err != nil {
		return BakeResult{}, err
	}
	res := BakeResult{Image: baked.Image, RepoIncluded: include, FinishLog: BakeFinishLogPath(name)}
	args := []string{"bake", "--finish"}
	if req.Events != "" {
		args = append(args, "--events", req.Events)
	}
	pid, err := spawnDetachedPID(req.RepoRoot, res.FinishLog, false, args...)
	if err != nil {
		// No background process: finish here rather than leave the image
		// and the bake instance to the sweeps.
		res.FinishLog = ""
		return res, c.FinishBake(ctx, req.RepoRoot, req.Progress)
	}
	// The finish is the bake's tail: BakeRunning stays true until it's done,
	// and so does the bake's job, which it carries on.
	_ = os.WriteFile(bakePIDPath(name), []byte(strconv.Itoa(pid)), 0o600)
	if req.Events != "" {
		if err := handOffJob(req.Events, pid); err != nil {
			c.logf("handing the bake's job to its finish: %v", err)
		} else {
			res.HandedOff = true
		}
	}
	return res, nil
}

// FinishBake is a bake's background half: wait until the image can launch
// sessions, release the bake instance, record the image, delete every image
// it replaces, and fill the pool from it.
func (c *Client) FinishBake(ctx context.Context, repoRoot string, progress Progress) error {
	start := time.Now()
	step := progress.stepper(start)
	name := filepath.Base(repoRoot)
	b, err := os.ReadFile(pendingBakePath(name))
	if err != nil {
		return fmt.Errorf("no bake of %s to finish: %w", name, err)
	}
	var p pendingBake
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("bake of %s: %w", name, err)
	}
	step("saving image " + p.Baked.Image + " — copying the bake disk into image storage")
	if err := c.drv.FinishBake(ctx, p.Baked, step); err != nil {
		if ctx.Err() == nil {
			os.Remove(pendingBakePath(name))
		}
		return err
	}
	os.Remove(pendingBakePath(name))
	var replaced []string
	// Update, not Save: the config was read before the bake started. Saving it
	// wholesale would revert anything written meanwhile — including another
	// repo's bake.
	next, err := config.Update(func(cfg *config.Config) error {
		replaced = cfg.BakedReplaces(name)
		cfg.RecordBake(name, p.Baked.Image)
		cfg.RecordImageInfo(name, p.Info)
		return nil
	})
	if err != nil {
		return err
	}
	c.setConf(next)
	step("image " + p.Baked.Image + " ready — new " + name + " sessions start from it")
	// Every earlier image of the repo goes: the config's previous pick (and
	// on aws-ec2 the legacy shared image), and any the config lost track of.
	if imgs, err := c.drv.Images(ctx); err == nil {
		for _, img := range imgs {
			if img.Repo == payload.Sanitize(name) {
				replaced = append(replaced, img.ID)
			}
		}
	} else {
		step("could not list images (" + err.Error() + ") — only the recorded previous image is removed")
	}
	seen := map[string]bool{p.Baked.Image: true, "": true}
	for _, id := range replaced {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := c.drv.DeleteImage(ctx, id); err != nil {
			step("could not delete superseded image " + id + ": " + err.Error())
			continue
		}
		step("deleted superseded image " + id)
	}
	// The pool fills now, not on the next create: its members were built on
	// the old image (the fill recycles them), and the next session should
	// find one waiting.
	if next.PoolSize(name) > 0 {
		log, err := c.SpawnFill(repoRoot)
		if err != nil {
			return fmt.Errorf("pool fill didn't start: %w — run `pier pool fill`", err)
		}
		step("filling the pool from the new image — log: " + log)
	}
	return nil
}

// poolParams assembles everything internal/pool needs, including the
// generation fingerprint — which is why it must run from inside the repo
// (the local setup script feeds the hash).
func (c *Client) poolParams(repoRoot string, progress Progress, start time.Time) (pool.Params, error) {
	maxAge, err := c.conf().PoolMaxAge()
	if err != nil {
		return pool.Params{}, err
	}
	idle, err := config.ParkDuration(c.conf().IdleTimeout)
	if err != nil {
		return pool.Params{}, err
	}
	cap_, err := config.ParkDuration(c.conf().UnattendedCap)
	if err != nil {
		return pool.Params{}, err
	}
	repo := filepath.Base(repoRoot)
	image := c.conf().BakedImage(repo)
	base := payload.KnownBase(repoRoot)
	return pool.Params{
		Driver:      c.drv,
		RepoRoot:    repoRoot,
		Base:        base,
		Gen:         pool.Generation(c.drv.Name(), image, c.MachineType(), c.DiskGiB(), pool.SetupScript(repoRoot, base)),
		Size:        c.conf().PoolSize(repo),
		MaxAge:      maxAge,
		Image:       image,
		IdleTimeout: idle, UnattendedCap: cap_,
		Manifest: c.conf().Secrets.Manifest, SessionEnv: SessionEnv(c.conf()),
		Mine:     c.HasKey,
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

// SetReady saves how many pooled sessions to keep for the repo at repoRoot,
// then reconciles: 0 destroys its pooled sessions now, more starts a
// background refill. Returns the refill log path ("" for 0).
func (c *Client) SetPoolSize(ctx context.Context, repoRoot string, n int, progress Progress) (string, error) {
	repo := filepath.Base(repoRoot)
	next, err := config.Update(func(cfg *config.Config) error {
		cfg.SetPoolSize(repo, n)
		return nil
	})
	if err != nil {
		return "", err
	}
	c.setConf(next)
	if n == 0 {
		_, err := c.DrainPool(ctx, repo, progress)
		return "", err
	}
	return c.SpawnFill(repoRoot)
}

// DrainReady destroys a repo's unclaimed pooled sessions (any repo; no local
// checkout needed) and returns how many went down.
func (c *Client) DrainPool(ctx context.Context, repo string, progress Progress) (int, error) {
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return 0, err
	}
	return pool.Drain(ctx, c.drv, sessions, repo, c.HasKey, progress.stepper(time.Now()))
}

// Refill tops the repo's pooled sessions up to target, in the foreground.
func (c *Client) FillPool(ctx context.Context, repoRoot string, progress Progress) error {
	if err := c.needImage(repoRoot); err != nil {
		return err
	}
	pp, err := c.poolParams(repoRoot, progress, time.Now())
	if err != nil {
		return err
	}
	err = pool.Fill(ctx, pp)
	recordFill(filepath.Base(repoRoot), err)
	return err
}

// FillLogPath is where a background refill of repo writes.
func PoolLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "pool-"+repo+".log")
}

// SpawnRefill re-execs the binary's hidden refill command as a detached
// child working in repoRoot, so a refill survives the frontend exiting.
func (c *Client) SpawnFill(repoRoot string) (string, error) {
	j, err := c.StartFill(repoRoot)
	return j.Log, err
}

// BakeLogPath is where a background bake of repo writes.
func BakeLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".log")
}

// SpawnBake runs `pier bake` for the repo at repoRoot in the background and
// returns its log path. BakeRunning reports on it while it runs.
func (c *Client) SpawnBake(repoRoot string) (string, error) {
	j, err := c.StartBake(repoRoot, nil)
	if err != nil {
		return BakeLogPath(filepath.Base(repoRoot)), err
	}
	return j.Log, nil
}

// BakeRunning reports whether a bake of repo started from this machine is
// still running, its background finish included.
func BakeRunning(repo string) bool { return bakePID(repo) != 0 }

// bakePID is the live process of repo's bake, 0 when none.
func bakePID(repo string) int {
	b, err := os.ReadFile(bakePIDPath(repo))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	return pid
}

// BakeFinishLogPath is where a bake's background finish writes.
func BakeFinishLogPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".finish.log")
}

func bakePIDPath(repo string) string {
	return filepath.Join(config.Dir(), "logs", "bake-"+repo+".pid")
}

// spawnDetachedPID starts the pier binary with args in its own session,
// output to logPath, working in dir, and returns its pid.

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

// Checkout is a linked local clone: what a frontend without a working
// directory creates sessions, bakes and fills pools from.
type Checkout struct {
	Path    string // repository root
	Name    string // its basename: the repo name sessions and images use
	Origin  string // canonical origin; "" for repos without one
	Missing bool   // the path no longer holds a git repository
}

// LinkCheckout records the repository containing dir as a linked checkout
// and returns it. Linking a path again refreshes its origin.
func (c *Client) LinkCheckout(dir string) (Checkout, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return Checkout{}, err
	}
	origin := payload.RepoOrigin(root)
	cfg, err := config.Update(func(cfg *config.Config) error {
		cfg.LinkCheckout(root, origin)
		return nil
	})
	if err != nil {
		return Checkout{}, err
	}
	c.setConf(cfg)
	return Checkout{Path: root, Name: filepath.Base(root), Origin: origin}, nil
}

// UnlinkCheckout forgets a linked checkout. The clone itself is untouched.
func (c *Client) UnlinkCheckout(root string) error {
	cfg, err := config.Update(func(cfg *config.Config) error {
		cfg.UnlinkCheckout(root)
		return nil
	})
	if err != nil {
		return err
	}
	c.setConf(cfg)
	return nil
}

// Checkouts lists the linked checkouts by name, each with its current
// origin (a remote can change after linking).
func (c *Client) Checkouts() []Checkout {
	out := make([]Checkout, 0, len(c.conf().Checkouts))
	for root, origin := range c.conf().Checkouts {
		co := Checkout{Path: root, Name: filepath.Base(root), Origin: origin}
		if got, err := RepoRoot(root); err != nil || got != root {
			co.Missing = true
		} else {
			co.Origin = payload.RepoOrigin(root)
		}
		out = append(out, co)
	}
	slices.SortFunc(out, func(a, b Checkout) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Path, b.Path))
	})
	return out
}

// RepoKey identifies the repository a session belongs to: its origin when
// recorded, so two clones of one repo group together and same-named folders
// of different repos don't; "name:<repo>" for sessions from before origins
// were recorded, or from repos without one.
func RepoKey(s Session) string {
	if s.Origin != "" {
		return s.Origin
	}
	return "name:" + s.Repo
}

// CheckoutKey is RepoKey for a linked checkout.
func CheckoutKey(co Checkout) string {
	if co.Origin != "" {
		return co.Origin
	}
	return "name:" + co.Name
}

// CheckoutInfo is what a create from a checkout's HEAD would carry.
type CheckoutInfo struct {
	Checkout
	Branch string // checked-out branch, "" when detached
	Head   string // short commit
	Dirty  int    // uncommitted changes: tracked edits and untracked files
	Base   string // the origin branch a session from HEAD builds on
	// DefaultBranch is origin's default branch: where a session starts,
	// and its pull request targets, unless told otherwise.
	DefaultBranch string
}

// InspectCheckout describes the repository containing dir for a frontend
// about to create a session from it.
func InspectCheckout(dir string) (CheckoutInfo, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return CheckoutInfo{}, err
	}
	git := func(args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	info := CheckoutInfo{
		Checkout:      Checkout{Path: root, Name: filepath.Base(root), Origin: payload.RepoOrigin(root)},
		Branch:        git("symbolic-ref", "--short", "-q", "HEAD"),
		Head:          git("rev-parse", "--short", "HEAD"),
		Base:          payload.BaseBranch(root, "HEAD"),
		DefaultBranch: payload.DefaultBranch(root),
	}
	if st := git("status", "--porcelain"); st != "" {
		info.Dirty = len(strings.Split(st, "\n"))
	}
	return info, nil
}

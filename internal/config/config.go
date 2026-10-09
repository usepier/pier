// Package config loads/saves ~/.config/pier/config.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Driver        string  `toml:"driver"`
	IdleTimeout   string  `toml:"idle_timeout"`   // duration or "never"
	UnattendedCap string  `toml:"unattended_cap"` // duration or "never"
	AWS           AWS     `toml:"aws"`
	GCP           GCP     `toml:"gcp"`
	Pool          Pool    `toml:"pool"`
	Speed         Speed   `toml:"speed"`
	Agents        Agents  `toml:"agents"`
	Prompts       Prompts `toml:"prompts,omitempty"`
	Theme         string  `toml:"theme,omitempty"` // accent color; see Themes
	Secrets       Secrets `toml:"secrets"`
	// Images records what each repo's session image was baked from — when,
	// and from which .pier scripts — so pier can remind (never decide) when a
	// rebake would help. Keyed by repo basename, like the image ids.
	Images map[string]ImageInfo `toml:"images,omitempty"`
	// Checkouts are local clones frontends without a working directory (the
	// Mac app) act from: creates, bakes and pool fills need one. Keyed by the
	// repo root's absolute path; the value is its canonical origin ("" for
	// repos without one).
	Checkouts map[string]string `toml:"checkouts,omitempty"`
}

// LinkCheckout records root, a repository root, as a local checkout of
// origin.
func (c *Config) LinkCheckout(root, origin string) {
	if c.Checkouts == nil {
		c.Checkouts = map[string]string{}
	}
	c.Checkouts[root] = origin
}

// UnlinkCheckout forgets a linked checkout.
func (c *Config) UnlinkCheckout(root string) { delete(c.Checkouts, root) }

// Speed holds the two settings a speed profile presets (pool size and bake
// reminders), plus how images
// are built and when pier suggests rebuilding them.
// Agents are how new chats start unless told otherwise.
type Agents struct {
	Default      string `toml:"default,omitempty"` // claude | codex
	ClaudeModel  string `toml:"claude_model,omitempty"`
	ClaudeEffort string `toml:"claude_effort,omitempty"`
	ClaudeMode   string `toml:"claude_mode,omitempty"`
	CodexModel   string `toml:"codex_model,omitempty"`
	CodexEffort  string `toml:"codex_effort,omitempty"`
	CodexMode    string `toml:"codex_mode,omitempty"`
}

// For is an agent's defaults: model, effort, permission mode.
func (a Agents) For(agent string) (model, effort, mode string) {
	if agent == "codex" {
		return a.CodexModel, a.CodexEffort, a.CodexMode
	}
	return a.ClaudeModel, a.ClaudeEffort, a.ClaudeMode
}

// Agent is the agent new chats use.
func (a Agents) Agent() string {
	if a.Default == "codex" {
		return "codex"
	}
	return "claude"
}

type Speed struct {
	// PoolSize is how many parked, setup-complete sessions pier keeps in each
	// baked repo's pool (a repo's own entry in pool.sizes overrides it).
	// Parked = disk-only cost while waiting.
	PoolSize int `toml:"pool_size"`
	// LegacyReadySessions is pool_size's name in one pre-release build; read
	// once at load so configs written by it keep their setting.
	LegacyReadySessions *int `toml:"ready_sessions,omitempty"`
	// BakeReminders: suggest `pier bake` when a repo has no image, its image
	// gets old, or its .pier scripts changed since the bake. Never automatic.
	BakeReminders bool `toml:"bake_reminders"`
	// ImageRepo: bakes include the repo checkout with .pier/setup.sh already
	// run (the fast default). false bakes toolchains only, for teams that keep
	// repo state out of images.
	ImageRepo bool `toml:"image_repo"`
	// ReminderAge is how old an image gets before pier suggests a rebake:
	// "30d", "72h", or "never".
	ReminderAge string `toml:"reminder_age"`
	// AutoRefill: keep pools at their size on their own — whenever a repo's
	// pool runs below it, a frontend that's running (the Mac app) fills it
	// in the background. Off: pools refill after a claim, a bake, or `pier
	// pool fill`.
	AutoRefill bool `toml:"auto_refill"`
}

// ImageInfo is what one repo's image was baked from.
type ImageInfo struct {
	BakedAt      time.Time `toml:"baked_at"`
	SetupSHA     string    `toml:"setup_sha,omitempty"` // sha256 of .pier/setup.sh at bake; "" = none
	BakeSHA      string    `toml:"bake_sha,omitempty"`  // sha256 of .pier/bake.sh at bake; "" = none
	RepoIncluded bool      `toml:"repo_included"`
}

type AWS struct {
	Profile      string `toml:"profile"`
	Region       string `toml:"region"`
	InstanceType string `toml:"instance_type"`
	DiskGiB      int    `toml:"disk_gib"`
	Subnet       string `toml:"subnet"` // optional: orgs without a default VPC
	// Direct (the default): ssh straight to the instance's public IP —
	// line-rate transfers, raw-RTT typing. pier opens TCP 22 from this
	// machine's public IP only and falls back to the SSM tunnel whenever
	// the direct path doesn't work. false forces the tunnel.
	Direct bool `toml:"direct"`
	// BakedAMI is the legacy shared image (pre repo-specific bakes) — still
	// used as a fallback, deregistered and cleared by the next `pier bake`.
	BakedAMI string `toml:"baked_ami,omitempty"`
	// BakedAMIs: repo basename -> image, written by `pier bake` (run from the
	// repo). Each repo bakes its own image so .pier/bake.sh toolchains don't
	// bleed across projects.
	BakedAMIs map[string]string `toml:"baked_amis,omitempty"`
}

type GCP struct {
	Project     string `toml:"project"`
	Zone        string `toml:"zone"`
	MachineType string `toml:"machine_type"`
	DiskGiB     int    `toml:"disk_gib"`
	// BakedImages: repo basename -> image, written by `pier bake` (run from
	// the repo). Each repo bakes its own image so .pier/bake.sh toolchains
	// don't bleed across projects.
	BakedImages map[string]string `toml:"baked_images,omitempty"`
}

// Pool configures repo-scoped warm session pools. Strictly opt-in: a pool
// exists only for repos with a size entry. Managed by `pier pool set`, not
// the TUI settings — like baked images, a pool is a per-repo cost decision.
type Pool struct {
	// MaxAge recycles members older than this, bounding how far a warm
	// checkout drifts from the default branch. Duration with a d unit
	// allowed ("14d", "72h"); empty means 14d.
	MaxAge string `toml:"max_age,omitempty"`
	// Sizes: repo basename -> desired warm member count.
	Sizes map[string]int `toml:"sizes,omitempty"`
}

type Secrets struct {
	// Manifest: files/dirs under $HOME copied one-way into each session's
	// home at create. Repo files listed in .pier/include travel
	// additionally (uncommitted tracked edits ride separately, as a patch).
	Manifest []string `toml:"manifest"`
	// ClaudeOAuthToken: from `claude setup-token` (macOS Keychain escape
	// hatch); injected as CLAUDE_CODE_OAUTH_TOKEN in sessions.
	ClaudeOAuthToken string `toml:"claude_oauth_token"`
}

func Default() Config {
	return Config{
		Driver:        "aws-ec2",
		IdleTimeout:   "30m",
		UnattendedCap: "8h",
		AWS: AWS{
			InstanceType: "t4g.medium",
			DiskGiB:      40,
			Direct:       true,
		},
		GCP: GCP{
			Zone:        "europe-west3-a",
			MachineType: "e2-medium",
			DiskGiB:     40,
		},
		Speed: Speed{PoolSize: 1, BakeReminders: true, ImageRepo: true, ReminderAge: "30d"},
	}
}

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pier", "config.toml")
}

func Dir() string { return filepath.Dir(Path()) }

// ErrNoConfig: pier was never set up on this machine.
var ErrNoConfig = errors.New("no config")

func Load() (Config, error) {
	c := Default()
	md, err := toml.DecodeFile(Path(), &c)
	if err != nil {
		if os.IsNotExist(err) {
			return c, fmt.Errorf("%w at %s — run `pier setup` first", ErrNoConfig, Path())
		}
		return c, err
	}
	if c.Speed.LegacyReadySessions != nil {
		if !md.IsDefined("speed", "pool_size") {
			c.Speed.PoolSize = *c.Speed.LegacyReadySessions
		}
		c.Speed.LegacyReadySessions = nil // the next write drops the old key
	}
	return c, nil
}

// Save writes the whole config, replacing whatever is on disk. It takes the
// lock so a write can't interleave with another process's, but it does NOT
// re-read first — so it reverts anything saved since this Config was loaded.
// Use Update for anything that changes part of the config; Save is for the
// setup wizard, which has just asked the user about all of it.
func (c Config) Save() error {
	unlock, err := lockConfig()
	if err != nil {
		return err
	}
	defer unlock()
	return c.write()
}

// write is Save without the lock, for callers already holding it.
//
// The file holds things no UI can put back: the baked-image map, the secrets
// manifest, the Claude token. Truncate-then-encode meant a write that died
// halfway left a short file that still parsed, and the previous contents were
// gone either way. So: encode to memory first, keep the outgoing version as
// .bak, and swap the new one in with a rename — a reader always sees one
// whole version or the other, and the last one stays recoverable.
func (c Config) write() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if old, err := os.ReadFile(Path()); err == nil && !bytes.Equal(old, buf.Bytes()) {
		// Best-effort: a config that can't be backed up still saves.
		_ = os.WriteFile(Path()+".bak", old, 0o600)
	}
	tmp, err := os.CreateTemp(Dir(), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path())
}

// gcp reports whether the active driver is gcp-gce (anything else falls to
// aws-ec2, matching newDriver's default).
func (c Config) gcp() bool { return c.Driver == "gcp-gce" }

// BakedImage is repo's baked image under the active driver ("" = launch from
// stock, guarded cloud-init installs live). aws-ec2 falls back to the legacy
// shared AMI from pre-repo-specific bakes.
func (c Config) BakedImage(repo string) string {
	if c.gcp() {
		return c.GCP.BakedImages[repo]
	}
	if img := c.AWS.BakedAMIs[repo]; img != "" {
		return img
	}
	return c.AWS.BakedAMI
}

// BakedReplaces lists the images a fresh bake of repo supersedes.
func (c Config) BakedReplaces(repo string) []string {
	if c.gcp() {
		return []string{c.GCP.BakedImages[repo]}
	}
	return []string{c.AWS.BakedAMIs[repo], c.AWS.BakedAMI}
}

// RecordBake stores repo's new image under the active driver. It merges into
// the map — replacing it would strand other repos' images as unreferenced
// (but still billing) artifacts.
func (c *Config) RecordBake(repo, image string) {
	if c.gcp() {
		if c.GCP.BakedImages == nil {
			c.GCP.BakedImages = map[string]string{}
		}
		c.GCP.BakedImages[repo] = image
		return
	}
	if c.AWS.BakedAMIs == nil {
		c.AWS.BakedAMIs = map[string]string{}
	}
	c.AWS.BakedAMIs[repo] = image
	c.AWS.BakedAMI = ""
}

// ForgetBake drops repo's image reference and its bake details under the
// active driver (the image itself is gone or superseded).
func (c *Config) ForgetBake(repo string) {
	if c.gcp() {
		delete(c.GCP.BakedImages, repo)
	} else {
		delete(c.AWS.BakedAMIs, repo)
	}
	delete(c.Images, repo)
}

// ClearBakes drops the active driver's baked-image references (teardown
// deleted the images themselves).
func (c *Config) ClearBakes() {
	if c.gcp() {
		c.GCP.BakedImages = nil
		return
	}
	c.AWS.BakedAMI = ""
	c.AWS.BakedAMIs = nil
	c.Images = nil
}

// PoolSize is how many sessions pier keeps in repo's pool: the repo's own
// entry when it has one, otherwise the speed default — but only for repos
// with a session image. Without one a pooled session would pay the full cold
// setup on every refill, which is the cost the reminder to bake points at.
func (c Config) PoolSize(repo string) int {
	if n, ok := c.Pool.Sizes[repo]; ok {
		return n
	}
	if c.BakedImage(repo) == "" {
		return 0
	}
	return c.Speed.PoolSize
}

// SetPoolSize records repo's pool size. A count equal to what the
// default would give removes the entry, so the repo follows later default
// changes; anything else (0 included) is pinned for this repo.
func (c *Config) SetPoolSize(repo string, n int) {
	if n < 0 {
		n = 0
	}
	delete(c.Pool.Sizes, repo)
	if c.PoolSize(repo) == n {
		return
	}
	if c.Pool.Sizes == nil {
		c.Pool.Sizes = map[string]int{}
	}
	c.Pool.Sizes[repo] = n
}

// PoolRepos lists every repo with a nonzero pool size: explicit
// entries plus baked repos riding the default.
func (c Config) PoolRepos() []string {
	seen := map[string]bool{}
	var out []string
	add := func(r string) {
		if !seen[r] && c.PoolSize(r) > 0 {
			seen[r] = true
			out = append(out, r)
		}
	}
	for r := range c.Pool.Sizes {
		add(r)
	}
	for r := range c.bakedRepos() {
		add(r)
	}
	return out
}

// BakedRepos lists the repos with an image under the active driver.
func (c Config) BakedRepos() []string {
	var out []string
	for r := range c.bakedRepos() {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// bakedRepos is the set of repos with an image under the active driver.
func (c Config) bakedRepos() map[string]bool {
	m := map[string]string{}
	if c.gcp() {
		m = c.GCP.BakedImages
	} else {
		m = c.AWS.BakedAMIs
	}
	out := map[string]bool{}
	for r, img := range m {
		if img != "" {
			out[r] = true
		}
	}
	return out
}

// Profile names the speed preset the current settings match: "fast"
// (reminders + a pool of 1), "lean" (reminders, no pool), "minimal"
// (neither), or "custom" for anything else.
func (c Config) Profile() string {
	switch {
	case c.Speed.BakeReminders && c.Speed.PoolSize == 1:
		return "fast"
	case c.Speed.BakeReminders && c.Speed.PoolSize == 0:
		return "lean"
	case !c.Speed.BakeReminders && c.Speed.PoolSize == 0:
		return "minimal"
	}
	return "custom"
}

// ApplyProfile sets the two settings a speed preset stands for.
func (c *Config) ApplyProfile(p string) error {
	switch p {
	case "fast":
		c.Speed.BakeReminders, c.Speed.PoolSize = true, 1
	case "lean":
		c.Speed.BakeReminders, c.Speed.PoolSize = true, 0
	case "minimal":
		c.Speed.BakeReminders, c.Speed.PoolSize = false, 0
	default:
		return fmt.Errorf("speed profile: want fast, lean or minimal (got %q)", p)
	}
	return nil
}

// ReminderAge parses speed.reminder_age; 0 = never remind about age.
func (c Config) ReminderAge() time.Duration {
	d, err := parseDays(c.Speed.ReminderAge)
	if err != nil {
		return 30 * 24 * time.Hour
	}
	return d
}

// RecordImageInfo stores what repo's new image was baked from.
func (c *Config) RecordImageInfo(repo string, info ImageInfo) {
	if c.Images == nil {
		c.Images = map[string]ImageInfo{}
	}
	c.Images[repo] = info
}

// PoolMaxAge parses pool.max_age, defaulting to 14 days. time.ParseDuration
// has no days unit, so "14d" is handled here (whole days only).
func (c Config) PoolMaxAge() (time.Duration, error) {
	s := c.Pool.MaxAge
	if s == "" {
		return 14 * 24 * time.Hour, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
		return 0, fmt.Errorf("pool.max_age: want a positive duration like 14d or 72h (got %q)", s)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("pool.max_age: want a positive duration like 14d or 72h (got %q)", s)
	}
	return d, nil
}

// parseDays reads "30d", "72h" or "never" ("" and "never" = 0).
func parseDays(s string) (time.Duration, error) {
	if s == "" || s == "never" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("want a positive duration like 30d or 72h (got %q)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("want a positive duration like 30d or 72h (got %q)", s)
	}
	return d, nil
}

// ParkDuration parses "30m" / "8h" / "never" (or "0") into a duration;
// 0 means disabled.
func ParkDuration(s string) (time.Duration, error) {
	if s == "" || s == "never" || s == "0" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// Package config loads/saves ~/.config/pier/config.toml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
	Secrets       Secrets `toml:"secrets"`
	// Images records what each repo's session image was baked from — when,
	// and from which .pier scripts — so pier can remind (never decide) when a
	// rebake would help. Keyed by repo basename, like the image ids.
	Images map[string]ImageInfo `toml:"images,omitempty"`
}

// Speed holds the two settings a speed profile presets, plus how images
// are built and when pier suggests rebuilding them.
type Speed struct {
	// ReadySessions is how many parked, setup-complete sessions pier keeps
	// ready for each repo that has a session image (a repo's own entry in
	// pool.sizes overrides it). Parked = disk-only cost while waiting.
	ReadySessions int `toml:"ready_sessions"`
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
// exists only for repos with a size entry. Managed by `pier ready`, not
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
		Speed: Speed{ReadySessions: 1, BakeReminders: true, ImageRepo: true, ReminderAge: "30d"},
	}
}

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pier", "config.toml")
}

func Dir() string { return filepath.Dir(Path()) }

func Load() (Config, error) {
	c := Default()
	if _, err := toml.DecodeFile(Path(), &c); err != nil {
		if os.IsNotExist(err) {
			return c, fmt.Errorf("no config at %s — run `pier setup` first", Path())
		}
		return c, err
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

// PoolSize is how many ready sessions pier keeps for repo: the repo's own
// entry when it has one, otherwise the speed default — but only for repos
// with a session image. Without one a ready session would pay the full cold
// setup on every refill, which is the cost the reminder to bake points at.
func (c Config) PoolSize(repo string) int {
	if n, ok := c.Pool.Sizes[repo]; ok {
		return n
	}
	if c.BakedImage(repo) == "" {
		return 0
	}
	return c.Speed.ReadySessions
}

// SetPoolSize records repo's ready-session count. A count equal to what the
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

// PoolRepos lists every repo with a nonzero ready-session target: explicit
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
// (reminders + 1 ready session), "lean" (reminders, none ready), "minimal"
// (neither), or "custom" for anything else.
func (c Config) Profile() string {
	switch {
	case c.Speed.BakeReminders && c.Speed.ReadySessions == 1:
		return "fast"
	case c.Speed.BakeReminders && c.Speed.ReadySessions == 0:
		return "lean"
	case !c.Speed.BakeReminders && c.Speed.ReadySessions == 0:
		return "minimal"
	}
	return "custom"
}

// ApplyProfile sets the two settings a speed preset stands for.
func (c *Config) ApplyProfile(p string) error {
	switch p {
	case "fast":
		c.Speed.BakeReminders, c.Speed.ReadySessions = true, 1
	case "lean":
		c.Speed.BakeReminders, c.Speed.ReadySessions = true, 0
	case "minimal":
		c.Speed.BakeReminders, c.Speed.ReadySessions = false, 0
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

// FieldKind tells the settings UI how to edit a field.
type FieldKind int

const (
	KindText    FieldKind = iota // free-text editor
	KindChoice                   // pick from the fixed Options list
	KindMachine                  // pick from the driver's machine catalog (injected)
)

// Option is one choice in a KindChoice picker. Value is what Set receives;
// Label is what the picker shows (falls back to Value); Desc is a dim
// right-column annotation (a human region name, machine specs, guidance).
type Option struct {
	Value string
	Label string
	Desc  string
}

// Field is one editable setting: how it's grouped, labeled, and explained,
// how it's edited (Kind + Options), and how its value reads back (Empty /
// Suffix). Validation always runs through Set, the single write path.
type Field struct {
	Key      string
	Group    string // a Groups key
	Cloud    string // "aws" / "gcp": applies only while that cloud is active; "" = always
	Label    string // human label shown on the row
	Hint     string // short one-liner beside the value
	Detail   string // multi-line (\n-split) footer for the selected field
	Default  string // shown in the footer; "" prints nothing
	Kind     FieldKind
	Options  []Option // KindChoice choices; nil otherwise
	NoCustom bool     // a closed enum: the picker offers no "custom…" escape
	Empty    string   // placeholder for an empty value ("" → "(unset)")
	Suffix   string   // appended to a raw value on display (e.g. " GiB")
}

// Display renders a stored value for humans: choice labels instead of raw
// values, a placeholder when empty, a unit suffix otherwise. Storage is
// untouched — this is presentation only.
func (f Field) Display(v string) string {
	if v == "" {
		if f.Empty != "" {
			return f.Empty
		}
		return "(unset)"
	}
	if f.Kind == KindChoice {
		for _, o := range f.Options {
			if o.Value == v {
				if o.Label != "" {
					return o.Label
				}
				return o.Value
			}
		}
	}
	return v + f.Suffix
}

// durationOpts / diskOpts are shared so the aws and gcp groups read the same.
var (
	idleOpts = []Option{{Value: "15m"}, {Value: "30m"}, {Value: "1h"}, {Value: "2h"}, {Value: "never"}}
	capOpts  = []Option{{Value: "4h"}, {Value: "8h"}, {Value: "24h"}, {Value: "never"}}
	diskOpts = []Option{{Value: "20", Label: "20 GiB"}, {Value: "40", Label: "40 GiB"}, {Value: "80", Label: "80 GiB"}, {Value: "160", Label: "160 GiB"}}
)

// Groups orders the settings page's sections. Every field belongs to one.
var Groups = []struct{ Key, Title string }{
	{"cloud", "Cloud"},
	{"sessions", "New sessions"},
	{"idle", "Idle & cost"},
	{"speed", "Speed"},
}

// Settings lists the settable fields in display order, grouped by what the
// user is thinking about when they open the page. Fields tagged with a Cloud
// only apply (and only show) while that cloud is active. Secrets and baked
// images are absent on purpose: `pier setup` and `pier bake` manage them.
var Settings = []Field{
	{
		Key: "driver", Group: "cloud", Label: "provider", Hint: "where sessions run",
		Kind: KindChoice, NoCustom: true, Default: "aws-ec2",
		Options: []Option{
			{Value: "aws-ec2", Label: "AWS", Desc: "Amazon EC2 · direct ssh or SSM"},
			{Value: "gcp-gce", Label: "GCP", Desc: "Google Compute Engine · IAP tunnel"},
		},
		Detail: "Which provider runs new sessions.\nExisting sessions stay on the cloud they were built on. Switching shows that cloud's settings here; run `pier doctor` afterwards to check its groundwork.",
	},
	{
		Key: "aws.profile", Group: "cloud", Cloud: "aws", Label: "CLI profile", Hint: "AWS CLI profile pier uses",
		Kind: KindText, Empty: "(default)",
		Detail: "The AWS CLI profile every pier command runs under — SSO, MFA and credentials all come from it.\nset one up with `aws configure`.",
	},
	{
		Key: "aws.region", Group: "cloud", Cloud: "aws", Label: "region", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "eu-central-1", Empty: "from the CLI profile",
		Options: []Option{
			{Value: "us-east-1", Desc: "N. Virginia"},
			{Value: "us-east-2", Desc: "Ohio"},
			{Value: "us-west-2", Desc: "Oregon"},
			{Value: "eu-west-1", Desc: "Ireland"},
			{Value: "eu-central-1", Desc: "Frankfurt"},
			{Value: "ap-south-1", Desc: "Mumbai"},
			{Value: "ap-southeast-1", Desc: "Singapore"},
			{Value: "ap-northeast-1", Desc: "Tokyo"},
		},
		Detail: "New session VMs launch here; existing sessions stay where they are.\nAfter switching, `pier doctor` checks the groundwork exists in the new region. Session images are per region: rebake there.",
	},
	{
		Key: "gcp.project", Group: "cloud", Cloud: "gcp", Label: "project", Hint: "project sessions live in",
		Kind:   KindText,
		Detail: "Sessions are created in this project — pier always passes it explicitly, never your active gcloud default. Required on GCP.",
	},
	{
		Key: "gcp.zone", Group: "cloud", Cloud: "gcp", Label: "zone", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "europe-west3-a",
		Options: []Option{
			{Value: "us-central1-a", Desc: "Iowa"},
			{Value: "us-east1-b", Desc: "S. Carolina"},
			{Value: "europe-west1-b", Desc: "Belgium"},
			{Value: "europe-west3-a", Desc: "Frankfurt"},
			{Value: "europe-west4-a", Desc: "Netherlands"},
			{Value: "asia-southeast1-a", Desc: "Singapore"},
		},
		Detail: "New session VMs launch in this zone; existing sessions stay put.",
	},
	{
		Key: "aws.direct", Group: "cloud", Cloud: "aws", Label: "connection", Hint: "direct is fastest; SSM if port 22 is blocked",
		Kind: KindChoice, NoCustom: true, Default: "direct ssh",
		Options: []Option{
			{Value: "true", Label: "direct ssh", Desc: "straight to the VM's public IP — full speed"},
			{Value: "false", Label: "SSM tunnel", Desc: "everything through SSM (~1 MB/s)"},
		},
		Detail: "How your terminal reaches the VM.\ndirect ssh dials the public IP at full speed, opens TCP 22 to your current IP only, and falls back to the SSM tunnel by itself when that's blocked.\nSSM tunnel forces the tunnel — for networks that block outbound 22 or orgs that disallow the ingress rule.",
	},
	{
		Key: "aws.subnet", Group: "cloud", Cloud: "aws", Label: "subnet", Hint: "only needed without a default VPC",
		Kind: KindText, Empty: "default VPC",
		Detail: "Only for accounts whose default VPC was deleted: session VMs launch in this subnet. Leave empty otherwise.\nformat: subnet-0abc123…",
	},
	{
		Key: "aws.instance_type", Group: "sessions", Cloud: "aws", Label: "machine", Hint: "VM for new sessions",
		Kind: KindMachine, Default: "t4g.medium",
		Detail: "New sessions start on this VM type. Undersize freely — `m` on the Sessions tab resizes a live session in about a minute, disk intact.",
	},
	{
		Key: "gcp.machine_type", Group: "sessions", Cloud: "gcp", Label: "machine", Hint: "VM for new sessions",
		Kind: KindMachine, Default: "e2-medium",
		Detail: "New sessions start on this machine type. Undersize freely — `m` on the Sessions tab resizes a live session in a couple of minutes, disk intact.",
	},
	{
		Key: "aws.disk_gib", Group: "sessions", Cloud: "aws", Label: "disk", Hint: "per-session disk; survives parking",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Root disk for each new session. It's what survives parking, and what a parked session costs: about $0.08-0.10 per GiB-month (40 GiB ≈ $3-4/mo).\nWith a prebuilt image, leave room for dependencies and container images.",
	},
	{
		Key: "gcp.disk_gib", Group: "sessions", Cloud: "gcp", Label: "disk", Hint: "per-session disk; survives parking",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Boot disk for each new session. It's what survives parking, and what a parked session costs (40 GiB ≈ $3-4/mo).",
	},
	{
		Key: "idle_timeout", Group: "idle", Label: "park after", Hint: "park when detached and quiet",
		Kind: KindChoice, Options: idleOpts, Default: "30m",
		Detail: "A session you've detached from that goes quiet this long parks itself: the VM stops and only its disk costs money. Attaching resumes it in ~20-60s with your tmux windows and agent conversations restored.\n`pier keep` exempts one session; --idle overrides one create.",
	},
	{
		Key: "unattended_cap", Group: "idle", Label: "runaway cap", Hint: "park even while the agent keeps working",
		Kind: KindChoice, Options: capOpts, Default: "8h",
		Detail: "Parks a session even while the agent is busy, once you've been detached this long — a looping agent can't burn compute for days.\n--cap overrides one create.",
	},
	{
		Key: "speed.profile", Group: "speed", Label: "profile", Hint: "preset for the two rows below",
		Kind: KindChoice, NoCustom: true, Default: "fast",
		Options: []Option{
			{Value: "fast", Label: "Fast", Desc: "image + 1 ready session per repo · ~25s starts"},
			{Value: "lean", Label: "Lean", Desc: "image only · ~1-2 min starts, image storage only"},
			{Value: "minimal", Label: "Minimal", Desc: "nothing stored · full setup every start, $0 idle"},
		},
		Detail: "A profile is a preset for bake reminders and ready sessions. Change either row and the profile reads custom.\nFast: new sessions claim a parked, set-up session (~25s); each ready session costs its parked disk.\nLean: new sessions boot from the repo's image and re-run setup warm.\nMinimal: nothing is stored; every session runs the full setup.",
	},
	{
		Key: "speed.ready_sessions", Group: "speed", Label: "ready sessions", Hint: "parked and set up, per baked repo",
		Kind: KindChoice, NoCustom: true, Default: "1",
		Options: []Option{
			{Value: "0", Label: "none"},
			{Value: "1", Desc: "the next session starts in ~25s"},
			{Value: "2", Desc: "two quick starts back to back"},
			{Value: "3"},
		},
		Detail: "How many parked, setup-complete sessions pier keeps for each repo that has a session image. They're stopped VMs: each costs only its disk while waiting, and runs only while being refilled.\nOverride one repo on the Repos tab.",
	},
	{
		Key: "speed.bake_reminders", Group: "speed", Label: "bake reminders", Hint: "suggest `pier bake` when it would help",
		Kind: KindChoice, NoCustom: true, Default: "on",
		Options: []Option{
			{Value: "true", Label: "on", Desc: "when a repo has no image, it gets old, or .pier scripts changed"},
			{Value: "false", Label: "off"},
		},
		Detail: "pier never rebakes on its own. With reminders on it points out when a bake would make sessions start faster.",
	},
	{
		Key: "speed.image_repo", Group: "speed", Label: "image contents", Hint: "what `pier bake` puts in a repo's image",
		Kind: KindChoice, NoCustom: true, Default: "toolchains + repo",
		Options: []Option{
			{Value: "true", Label: "toolchains + repo", Desc: "checkout with setup already run — fastest starts"},
			{Value: "false", Label: "toolchains only", Desc: "keeps repo state out of images"},
		},
		Detail: "toolchains + repo: the bake runs .pier/setup.sh on a checkout, scrubs every secret pier pushed, and images the result; sessions only fetch what changed and re-run setup warm.\ntoolchains only: images carry the harnesses and .pier/bake.sh tools; every session runs the full setup.\nApplies to the next `pier bake`.",
	},
	{
		Key: "speed.reminder_age", Group: "speed", Label: "rebake after", Hint: "remind when an image gets this old",
		Kind: KindChoice, Default: "30d",
		Options: []Option{{Value: "14d"}, {Value: "30d"}, {Value: "60d"}, {Value: "never"}},
		Detail:  "Once a repo's image is this old, pier suggests a rebake so new sessions start with current dependencies.",
	},
}

// Get returns the current value of a settable key ("" for unknown keys).
func Get(c Config, key string) string {
	switch key {
	case "driver":
		return c.Driver
	case "idle_timeout":
		return c.IdleTimeout
	case "unattended_cap":
		return c.UnattendedCap
	case "aws.profile":
		return c.AWS.Profile
	case "aws.region":
		return c.AWS.Region
	case "aws.instance_type":
		return c.AWS.InstanceType
	case "aws.disk_gib":
		return strconv.Itoa(c.AWS.DiskGiB)
	case "aws.subnet":
		return c.AWS.Subnet
	case "aws.direct":
		return strconv.FormatBool(c.AWS.Direct)
	case "gcp.project":
		return c.GCP.Project
	case "gcp.zone":
		return c.GCP.Zone
	case "gcp.machine_type":
		return c.GCP.MachineType
	case "gcp.disk_gib":
		return strconv.Itoa(c.GCP.DiskGiB)
	case "speed.profile":
		return c.Profile()
	case "speed.ready_sessions":
		return strconv.Itoa(c.Speed.ReadySessions)
	case "speed.bake_reminders":
		return strconv.FormatBool(c.Speed.BakeReminders)
	case "speed.image_repo":
		return strconv.FormatBool(c.Speed.ImageRepo)
	case "speed.reminder_age":
		return c.Speed.ReminderAge
	}
	return ""
}

// Visible reports whether a field applies under the config's active cloud.
func (c Config) Visible(f Field) bool {
	switch f.Cloud {
	case "aws":
		return !c.gcp()
	case "gcp":
		return c.gcp()
	}
	return true
}

// Set mutates one whitelisted scalar, validating it before it lands. A
// rejected value never touches the config, so the caller can keep the field
// open for a fix. Changes apply to new sessions only.
func Set(c *Config, key, val string) error {
	switch key {
	case "driver":
		// Strict: a typo like "gcp" used to save and then silently fall back to
		// the AWS driver at create time. Common aliases normalize; the rest is
		// a hard error here, not a surprise minutes later.
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "aws-ec2", "aws", "ec2":
			c.Driver = "aws-ec2"
		case "gcp-gce", "gcp", "gce", "google":
			c.Driver = "gcp-gce"
		default:
			return fmt.Errorf("driver: want aws-ec2 or gcp-gce (got %q)", val)
		}
	case "idle_timeout", "unattended_cap":
		d, err := ParkDuration(val)
		if err != nil {
			return fmt.Errorf("%s: %v (want a duration like 30m or 8h, or never)", key, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: must not be negative (got %q)", key, val)
		}
		if key == "idle_timeout" {
			c.IdleTimeout = val
		} else {
			c.UnattendedCap = val
		}
	case "aws.profile":
		c.AWS.Profile = strings.TrimSpace(val)
	case "aws.region":
		val = strings.TrimSpace(val)
		if !validRegionish(val) {
			return fmt.Errorf("aws.region: want a region like us-east-1 or eu-central-1 (got %q)", val)
		}
		c.AWS.Region = val
	case "aws.instance_type":
		val = strings.TrimSpace(val)
		if !validAWSType(val) {
			return fmt.Errorf("aws.instance_type: want a type like t4g.medium (got %q)", val)
		}
		c.AWS.InstanceType = val
	case "aws.disk_gib":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 8 {
			return fmt.Errorf("aws.disk_gib: want a whole number of GiB, at least 8 (got %q)", val)
		}
		c.AWS.DiskGiB = n
	case "aws.subnet":
		val = strings.TrimSpace(val)
		if val != "" && !strings.HasPrefix(val, "subnet-") {
			return fmt.Errorf("aws.subnet: want a subnet id like subnet-0abc123, or empty for the default VPC (got %q)", val)
		}
		c.AWS.Subnet = val
	case "gcp.project":
		val = strings.TrimSpace(val)
		if val != "" && !validProjectID(val) {
			return fmt.Errorf("gcp.project: want a project id — lowercase letters, digits, hyphens, 6-30 chars (got %q)", val)
		}
		c.GCP.Project = val
	case "gcp.zone":
		val = strings.TrimSpace(val)
		if !validRegionish(val) {
			return fmt.Errorf("gcp.zone: want a zone like europe-west3-a or us-central1-a (got %q)", val)
		}
		c.GCP.Zone = val
	case "gcp.machine_type":
		val = strings.TrimSpace(val)
		if !validGCPType(val) {
			return fmt.Errorf("gcp.machine_type: want a type like e2-medium or e2-standard-4 (got %q)", val)
		}
		c.GCP.MachineType = val
	case "gcp.disk_gib":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 10 {
			return fmt.Errorf("gcp.disk_gib: want a whole number of GiB, at least 10 (got %q)", val)
		}
		c.GCP.DiskGiB = n
	case "aws.direct":
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "on":
			c.AWS.Direct = true
		case "false", "no", "off":
			c.AWS.Direct = false
		default:
			return fmt.Errorf("aws.direct: want true or false (got %q)", val)
		}
	case "speed.profile":
		return c.ApplyProfile(strings.ToLower(strings.TrimSpace(val)))
	case "speed.ready_sessions":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 0 || n > 8 {
			return fmt.Errorf("speed.ready_sessions: want 0-8 (got %q)", val)
		}
		c.Speed.ReadySessions = n
	case "speed.bake_reminders", "speed.image_repo":
		var b bool
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "on":
			b = true
		case "false", "no", "off":
		default:
			return fmt.Errorf("%s: want on or off (got %q)", key, val)
		}
		if key == "speed.bake_reminders" {
			c.Speed.BakeReminders = b
		} else {
			c.Speed.ImageRepo = b
		}
	case "speed.reminder_age":
		val = strings.TrimSpace(val)
		if _, err := parseDays(val); err != nil {
			return fmt.Errorf("speed.reminder_age: %v, or never", err)
		}
		c.Speed.ReminderAge = val
	default:
		keys := make([]string, len(Settings))
		for i, s := range Settings {
			keys[i] = s.Key
		}
		return fmt.Errorf("unknown key %q — settable: %s", key, strings.Join(keys, ", "))
	}
	return nil
}

// validRegionish is a lenient guard for AWS regions and GCP zones: a lowercase
// token with a hyphen and a digit (us-east-1, europe-west3-a, us-gov-west-1).
// Deliberately loose — it catches spaces, capitals, and wrong-field typos
// without rejecting valid or future names the strict picker doesn't list.
func validRegionish(s string) bool {
	if s == "" || !strings.Contains(s, "-") {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '-':
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasDigit
}

// validAWSType matches a family.size token (t4g.medium, m7g.4xlarge).
func validAWSType(s string) bool {
	fam, size, ok := strings.Cut(s, ".")
	return ok && lowerAlnum(fam) && lowerAlnum(size)
}

// validGCPType matches a hyphenated machine type (e2-medium, e2-standard-4,
// e2-custom-4-8192).
func validGCPType(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' || !strings.Contains(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// validProjectID is the shape of a GCP project id: 6-30 chars, starts with a
// letter, lowercase letters/digits/hyphens, no trailing hyphen.
func validProjectID(s string) bool {
	if len(s) < 6 || len(s) > 30 || s[0] < 'a' || s[0] > 'z' || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func lowerAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

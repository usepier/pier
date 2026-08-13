// Package config loads/saves ~/.config/pier/config.toml.
package config

import (
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
	Secrets       Secrets `toml:"secrets"`
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

func (c Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
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
	Group    string // "session", "aws", "gcp"
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

// Settings lists the settable fields in display order, grouped session → aws →
// gcp. Secrets and baked images are deliberately absent: those are managed by
// `pier setup` and `pier bake`, and the TUI shows them read-only.
var Settings = []Field{
	{
		Key: "driver", Group: "session", Label: "cloud", Hint: "runs new sessions",
		Kind: KindChoice, NoCustom: true, Default: "aws-ec2",
		Options: []Option{
			{Value: "aws-ec2", Label: "AWS EC2", Desc: "Amazon EC2 · direct ssh or SSM"},
			{Value: "gcp-gce", Label: "GCP Compute Engine", Desc: "Google Compute Engine · IAP tunnel"},
		},
		Detail: "Which provider runs new sessions.\nExisting sessions stay on the cloud they were built on; the other cloud's settings wait dimmed below until you switch.",
	},
	{
		Key: "idle_timeout", Group: "session", Label: "auto-park", Hint: "park when detached & quiet",
		Kind: KindChoice, Options: idleOpts, Default: "30m",
		Detail: "A session detached and quiet this long parks itself: the VM stops, the disk stays (~$3-4/mo), and attaching resumes it in ~20-60s.\n`pier keep` exempts one session; --idle overrides one create.\nformat: 30m, 2h, or never",
	},
	{
		Key: "unattended_cap", Group: "session", Label: "unattended cap", Hint: "park even mid-run",
		Kind: KindChoice, Options: capOpts, Default: "8h",
		Detail: "Parks a session even while the agent is still busy, once you've been detached this long — a runaway loop can't burn compute for days.\n--cap overrides one create.\nformat: 8h or never",
	},
	{
		Key: "aws.profile", Group: "aws", Label: "profile", Hint: "AWS CLI profile for every call",
		Kind:   KindText,
		Detail: "The AWS CLI profile every pier command runs under — SSO, MFA, and its default region all come from it.\nset one up with `aws configure`.",
	},
	{
		Key: "aws.region", Group: "aws", Label: "region", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "eu-central-1",
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
		Detail: "New session VMs launch here; existing sessions stay where they are.\nafter switching, `pier doctor` checks the groundwork exists in the new region.\nformat: us-east-1, eu-central-1, …",
	},
	{
		Key: "aws.instance_type", Group: "aws", Label: "machine", Hint: "default VM for new sessions",
		Kind: KindMachine, Default: "t4g.medium",
		Detail: "New sessions start on this VM type. Undersize freely — `m` resizes any live session in about a minute, disk intact.",
	},
	{
		Key: "aws.disk_gib", Group: "aws", Label: "disk", Hint: "per-session root disk",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Root disk for each new session — it's what survives parking and what a parked session costs (~$3-4/mo).\nwhole GiB, at least 8",
	},
	{
		Key: "aws.direct", Group: "aws", Label: "connection", Hint: "how ssh reaches the VM",
		Kind: KindChoice, NoCustom: true, Default: "direct ssh",
		Options: []Option{
			{Value: "true", Label: "direct ssh", Desc: "straight to the VM's public IP — full speed"},
			{Value: "false", Label: "SSM tunnel", Desc: "everything through SSM (~1 MB/s)"},
		},
		Detail: "How the terminal reaches the VM.\ndirect ssh dials the public IP (full speed), opens TCP 22 to your current IP only, and falls back to the SSM tunnel by itself when that's blocked.\nSSM tunnel forces the tunnel — for networks that block outbound 22 or orgs that disallow the ingress calls.",
	},
	{
		Key: "aws.subnet", Group: "aws", Label: "subnet", Hint: "only without a default VPC",
		Kind: KindText, Empty: "(default VPC)",
		Detail: "Only for accounts whose default VPC was deleted: session VMs launch in this subnet. Leave empty otherwise.\nformat: subnet-0abc123…",
	},
	{
		Key: "gcp.project", Group: "gcp", Label: "project", Hint: "project sessions are created in",
		Kind:   KindText,
		Detail: "Sessions are created in this project — pier always passes it explicitly, never your active gcloud default. Required when cloud is GCP.",
	},
	{
		Key: "gcp.zone", Group: "gcp", Label: "zone", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "europe-west3-a",
		Options: []Option{
			{Value: "us-central1-a", Desc: "Iowa"},
			{Value: "us-east1-b", Desc: "S. Carolina"},
			{Value: "europe-west1-b", Desc: "Belgium"},
			{Value: "europe-west3-a", Desc: "Frankfurt"},
			{Value: "europe-west4-a", Desc: "Netherlands"},
			{Value: "asia-southeast1-a", Desc: "Singapore"},
		},
		Detail: "New session VMs launch in this zone; existing sessions stay put.\nformat: europe-west3-a, us-central1-a, …",
	},
	{
		Key: "gcp.machine_type", Group: "gcp", Label: "machine", Hint: "default VM for new sessions",
		Kind: KindMachine, Default: "e2-medium",
		Detail: "New sessions start on this machine type. Undersize freely — `m` resizes any live session in a couple of minutes, disk intact.",
	},
	{
		Key: "gcp.disk_gib", Group: "gcp", Label: "disk", Hint: "per-session boot disk",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Boot disk for each new session — it's what survives parking and what a parked session costs (~$3-4/mo).\nwhole GiB, at least 10",
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
	}
	return ""
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

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
	// repo). Each repo bakes its own image so .pier-bake.sh toolchains don't
	// bleed across projects.
	BakedAMIs map[string]string `toml:"baked_amis,omitempty"`
}

type GCP struct {
	Project     string `toml:"project"`
	Zone        string `toml:"zone"`
	MachineType string `toml:"machine_type"`
	DiskGiB     int    `toml:"disk_gib"`
	// BakedImages: repo basename -> image, written by `pier bake` (run from
	// the repo). Each repo bakes its own image so .pier-bake.sh toolchains
	// don't bleed across projects.
	BakedImages map[string]string `toml:"baked_images,omitempty"`
}

type Secrets struct {
	// Manifest: files/dirs under $HOME copied one-way into each session's
	// home at create. Repo files listed in a repo-root .pier-include travel
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

// Setting is one field the TUI settings page can edit.
type Setting struct {
	Key  string
	Hint string
}

// Settings lists the settable keys in display order. Secrets and baked AMIs
// are deliberately absent: those are managed by `pier setup` and `pier bake`.
var Settings = []Setting{
	{"driver", ""},
	{"idle_timeout", "detached and quiet this long → park"},
	{"unattended_cap", "parks even while busy"},
	{"aws.profile", ""},
	{"aws.region", ""},
	{"aws.instance_type", "machine for new sessions"},
	{"aws.disk_gib", ""},
	{"aws.subnet", "optional"},
	{"aws.direct", "ssh straight to the VM, fast — false forces the ssm tunnel"},
	{"gcp.project", ""},
	{"gcp.zone", ""},
	{"gcp.machine_type", "machine for new sessions"},
	{"gcp.disk_gib", ""},
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

// Set mutates one whitelisted scalar, validating durations and disk size.
// Changes apply to new sessions only.
func Set(c *Config, key, val string) error {
	switch key {
	case "driver":
		c.Driver = val
	case "idle_timeout", "unattended_cap":
		if _, err := ParkDuration(val); err != nil {
			return fmt.Errorf("%s: %v (want a duration like 30m or 8h, or never)", key, err)
		}
		if key == "idle_timeout" {
			c.IdleTimeout = val
		} else {
			c.UnattendedCap = val
		}
	case "aws.profile":
		c.AWS.Profile = val
	case "aws.region":
		c.AWS.Region = val
	case "aws.instance_type":
		c.AWS.InstanceType = val
	case "aws.disk_gib":
		n, err := strconv.Atoi(val)
		if err != nil || n < 8 {
			return fmt.Errorf("aws.disk_gib: want a whole number of GiB, at least 8 (got %q)", val)
		}
		c.AWS.DiskGiB = n
	case "aws.subnet":
		c.AWS.Subnet = val
	case "gcp.project":
		c.GCP.Project = val
	case "gcp.zone":
		c.GCP.Zone = val
	case "gcp.machine_type":
		c.GCP.MachineType = val
	case "gcp.disk_gib":
		n, err := strconv.Atoi(val)
		if err != nil || n < 10 {
			return fmt.Errorf("gcp.disk_gib: want a whole number of GiB, at least 10 (got %q)", val)
		}
		c.GCP.DiskGiB = n
	case "aws.direct":
		switch strings.ToLower(val) {
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

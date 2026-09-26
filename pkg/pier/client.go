// Package pier is the one API every pier frontend uses: the CLI, the TUI,
// and (through `pier api`) the Mac app. It owns every capability — sessions,
// repos and their images, ready sessions, settings, setup checks — and never
// prints, exits, or reads stdin. Frontends render what it returns; long
// operations report progress as Events through a callback.
//
// Interactive steps that need a terminal (attach, MCP logins, log follows,
// port forwards) come back as prepared *exec.Cmd values, so each frontend
// runs them its own way: the CLI in the foreground, the TUI via
// tea.ExecProcess, the Mac app in a terminal view.
package pier

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/awsec2"
	"github.com/usepier/pier/internal/driver/gcpgce"
	"github.com/usepier/pier/internal/driver/payload"
)

// Re-exported so frontends name pier's types without importing internals.
type (
	Session = driver.Session
	State   = driver.State
	Machine = driver.Machine
	Check   = driver.Check
	Quota   = driver.Quota
)

const (
	StateCreating = driver.StateCreating
	StateRunning  = driver.StateRunning
	StateWorking  = driver.StateWorking
	StateIdle     = driver.StateIdle
	StateParked   = driver.StateParked
	StateDeleting = driver.StateDeleting
	StateDead     = driver.StateDead
	StateFailed   = driver.StateFailed
)

// Options carries what only the binary knows.
type Options struct {
	// SupervisorBin returns the embedded in-VM supervisor for an arch
	// ("arm64"/"amd64"). The binary embeds it at build time; tests and
	// read-only frontends may leave it nil.
	SupervisorBin func(arch string) ([]byte, error)
	// Version is the pier release.
	Version string
	// Out receives transfer meters and streamed remote output (the bake
	// hook's log). A terminal frontend passes os.Stdout; nil discards, which
	// is what a frontend speaking a protocol on stdout must choose.
	Out io.Writer
	// Notify receives one-off transport notices ("using the ssm tunnel").
	Notify func(string)
}

// Client is a configured pier: the user's config plus the cloud driver it
// selects. Cheap to build; build a fresh one after settings change.
type Client struct {
	cfg  config.Config
	drv  driver.Driver
	opts Options
}

// Open loads the user's config and returns a client for its cloud.
func Open(opts Options) (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return New(cfg, opts)
}

// New returns a client for an explicit config (the setup wizard builds one
// before any config is saved).
func New(cfg config.Config, opts Options) (*Client, error) {
	drv, err := NewDriver(cfg, opts)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, drv: drv, opts: opts}, nil
}

// NewDriver builds the cloud driver a config selects.
func NewDriver(cfg config.Config, opts Options) (driver.Driver, error) {
	sup := opts.SupervisorBin
	if sup == nil {
		sup = func(string) ([]byte, error) {
			return nil, fmt.Errorf("this build carries no session supervisor — build pier with `make`")
		}
	}
	switch cfg.Driver {
	case "", "aws-ec2":
		return &awsec2.Driver{
			Profile:       cfg.AWS.Profile,
			Region:        cfg.AWS.Region,
			InstanceType:  cfg.AWS.InstanceType,
			DiskGiB:       cfg.AWS.DiskGiB,
			Subnet:        cfg.AWS.Subnet,
			Direct:        cfg.AWS.Direct,
			StateDir:      config.Dir(),
			Manifest:      cfg.Secrets.Manifest,
			SessionEnv:    SessionEnv(cfg),
			SupervisorBin: sup,
			Out:           opts.Out,
			Notify:        opts.Notify,
		}, nil
	case "gcp-gce":
		// An empty project would fall through to the operator's active gcloud
		// config — pier must never create resources in whatever project
		// happens to be active.
		if cfg.GCP.Project == "" {
			return nil, fmt.Errorf("gcp.project is not set — run `pier setup`")
		}
		return &gcpgce.Driver{
			Project:       cfg.GCP.Project,
			Zone:          cfg.GCP.Zone,
			MachineType:   cfg.GCP.MachineType,
			DiskGiB:       cfg.GCP.DiskGiB,
			StateDir:      config.Dir(),
			Manifest:      cfg.Secrets.Manifest,
			SessionEnv:    SessionEnv(cfg),
			SupervisorBin: sup,
			Out:           opts.Out,
			Notify:        opts.Notify,
		}, nil
	default:
		return nil, fmt.Errorf("unknown driver %q", cfg.Driver)
	}
}

// SessionEnv builds ~/.config/pier/env for new sessions: a GitHub credential
// from wherever the laptop already has one (gh login or git's credential
// helper), the Claude token from config (macOS keychain escape hatch).
func SessionEnv(cfg config.Config) map[string]string {
	env := map[string]string{}
	if t := cfg.Secrets.ClaudeOAuthToken; t != "" {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = t
	}
	if t := payload.GitHubToken(); t != "" {
		env["GH_TOKEN"] = t
	}
	return env
}

// Config is the configuration this client was built from.
func (c *Client) Config() config.Config { return c.cfg }

// Driver exposes the cloud driver for the few features that manage their
// own processes against it (pier proxy).
func (c *Client) Driver() driver.Driver { return c.drv }

// Cloud names the active cloud for display: "AWS eu-central-1".
func (c *Client) Cloud() string {
	if c.cfg.Driver == "gcp-gce" {
		return "GCP " + c.cfg.GCP.Zone
	}
	return "AWS " + c.cfg.AWS.Region
}

// MachineType is the configured default machine for new sessions.
func (c *Client) MachineType() string {
	if c.cfg.Driver == "gcp-gce" {
		return c.cfg.GCP.MachineType
	}
	return c.cfg.AWS.InstanceType
}

// DiskGiB is the configured per-session disk.
func (c *Client) DiskGiB() int {
	if c.cfg.Driver == "gcp-gce" {
		return c.cfg.GCP.DiskGiB
	}
	return c.cfg.AWS.DiskGiB
}

// IsAuthExpired recognizes a cloud login expiry an interactive login fixes.
func IsAuthExpired(err error) bool { return awsec2.LoginExpired(err) }

// ReauthCommand is the foreground login that fixes IsAuthExpired errors.
func (c *Client) ReauthCommand() *exec.Cmd {
	args := []string{"login"}
	if c.cfg.AWS.Profile != "" {
		args = append(args, "--profile", c.cfg.AWS.Profile)
	}
	return exec.Command("aws", args...)
}

// RepoRoot returns the git toplevel containing dir ("" = the process cwd).
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", ErrNotInRepo
	}
	return strings.TrimSpace(string(out)), nil
}

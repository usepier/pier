// Package pier is the one API every pier frontend uses: the CLI, the TUI,
// and (through `pier api`) the Mac app. It owns every capability — sessions,
// repos and their images, pooled sessions, settings, setup checks — and never
// prints, exits, or reads stdin. Frontends render what it returns; long
// operations report progress as Events through a callback.
//
// Interactive steps that need a terminal (attach, MCP logins, log follows,
// port forwards) come back as prepared *exec.Cmd values, so each frontend
// runs them its own way: the CLI in the foreground, the TUI via
// tea.ExecProcess, the Mac app in a terminal view.
package pier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/awsec2"
	"github.com/usepier/pier/internal/driver/gcpgce"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/sshmux"
)

// Re-exported so frontends name pier's types without importing internals.
type (
	Session   = driver.Session
	State     = driver.State
	Machine   = driver.Machine
	Resources = driver.Resources
	Port      = driver.Port
	Check     = driver.Check
	Quota     = driver.Quota
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
	// Mux keeps one multiplexed ssh connection per session for long-lived
	// frontends (the api, the app): terminals and streams open one, and
	// status reads ride whatever is open. Close the client when done.
	Mux bool
}

// Client is a configured pier: the user's config plus the cloud driver it
// selects. Cheap to build; build a fresh one after settings change.
type Client struct {
	cfg  config.Config
	drv  driver.Driver
	opts Options
	mux  *sshmux.Manager // nil unless Options.Mux

	openMu  sync.Mutex
	openers map[openerKey]func() // browser openers forwarded per connection
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
	c := &Client{cfg: cfg, drv: drv, opts: opts}
	if opts.Mux {
		c.mux = sshmux.New(filepath.Join(config.Dir(), "mux"), drv.SSHTarget)
		c.mux.Invalidate = drv.InvalidateTarget
		c.mux.IdleClose = 5 * time.Minute
		c.mux.CleanStale()
	}
	return c, nil
}

// Close ends the client's ssh connections. Clients without Options.Mux hold
// none.
func (c *Client) Close() {
	if c.mux != nil {
		c.mux.Close()
	}
	c.openMu.Lock()
	for _, closeOpener := range c.openers {
		closeOpener()
	}
	c.openers = nil
	c.openMu.Unlock()
}

// Conn returns the session's multiplexed connection, opening it if needed:
// for long-lived users (terminals, streams). Needs Options.Mux.
func (c *Client) Conn(ctx context.Context, s Session) (*sshmux.Conn, error) {
	if c.mux == nil {
		return nil, fmt.Errorf("this client keeps no connections (Options.Mux is off)")
	}
	if err := CheckReady(s); err != nil {
		return nil, err
	}
	return c.mux.Conn(ctx, s.ID)
}

// exec runs a one-shot command in a session: over its open connection when
// there is one, else as a fresh ssh — a status read never opens a
// connection just for itself.
func (c *Client) exec(ctx context.Context, id, script string) (string, error) {
	if c.mux != nil {
		if conn := c.mux.Existing(id); conn != nil {
			out, err := conn.Exec(ctx, script, nil)
			return string(out), err
		}
	}
	return c.drv.Exec(ctx, id, script)
}

// Forget closes a session's connection (it parked or was removed).
func (c *Client) forget(id string) {
	if c.mux != nil {
		c.mux.Drop(id)
	}
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

// IsAuthExpired recognizes a cloud error an interactive login fixes, on
// either cloud: an expired or missing CLI login.
func IsAuthExpired(err error) bool {
	var le *driver.LoginError
	return errors.As(err, &le)
}

// ReauthCommand is the foreground login that fixes err, an IsAuthExpired
// error; nil for any other error.
func (c *Client) ReauthCommand(err error) *exec.Cmd {
	var le *driver.LoginError
	if !errors.As(err, &le) || len(le.Login) == 0 {
		return nil
	}
	return exec.Command(le.Login[0], le.Login[1:]...)
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

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/awsec2"
	"github.com/usepier/pier/internal/driver/gcpgce"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/github"
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
	// GitHub is the GitHub client to use; nil makes one (NewGitHub). A
	// process that builds clients again (after a settings change) passes
	// one, so its token cache and rate limit carry over.
	GitHub *github.Client
	// Log receives diagnostics nobody is waiting on (a copy kept for a
	// parked session that couldn't be written). nil discards them.
	Log func(format string, args ...any)
	// Mux keeps one multiplexed ssh connection per session for long-lived
	// frontends (the api, the app): terminals and streams open one, and
	// status reads ride whatever is open. Close the client when done.
	Mux bool
}

// Client is a configured pier: the user's config plus the cloud driver it
// selects. Cheap to build; build a fresh one when Stale says its driver no
// longer matches the config.
type Client struct {
	// cfg is read through conf: other processes change the file under a
	// long-lived client (a bake's background finish records its image,
	// another frontend changes a setting), and every read sees that.
	cfgMu    sync.Mutex
	cfg      config.Config
	cfgStamp fileStamp
	watchCfg bool   // cfg came from the file, so follows it
	drvKey   string // what the driver was built from (driverKey)

	drv  driver.Driver
	opts Options
	mux  *sshmux.Manager // nil unless Options.Mux
	gh   *github.Client

	openMu  sync.Mutex
	openers map[openerKey]func() // browser openers forwarded per connection

	// upgrades: one supervisor install per session at a time, and when
	// each last finished (upgrade).
	upgradeMu sync.Mutex
	upgrades  map[string]*sessionUpgrade
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
	c := &Client{cfg: cfg, cfgStamp: stampOf(config.Path()), watchCfg: true, drvKey: driverKey(cfg), drv: drv, opts: opts, gh: opts.GitHub}
	if c.gh == nil {
		c.gh = NewGitHub()
	}
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

// logf reports a diagnostic, if anyone listens.
func (c *Client) logf(format string, args ...any) {
	if c.opts.Log != nil {
		c.opts.Log(format, args...)
	}
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
func (c *Client) Config() config.Config { return c.conf() }

// fileStamp tells one version of a file from the next. The config is
// written to a new file renamed into place, so each version is also a new
// inode, however close together two writes land.
type fileStamp struct {
	ino  uint64
	mod  int64
	size int64
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	st := fileStamp{mod: fi.ModTime().UnixNano(), size: fi.Size()}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st
}

// ConfigStamp tells one version of the config file from the next: a
// frontend compares it to notice another process's change.
func ConfigStamp() any { return stampOf(config.Path()) }

// conf is the config as it now stands on disk.
func (c *Client) conf() config.Config {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	if c.watchCfg {
		if st := stampOf(config.Path()); st != c.cfgStamp {
			// A file caught mid-write fails to parse: keep the last good
			// copy, and read it again next time.
			if cfg, err := config.Load(); err == nil {
				c.cfg, c.cfgStamp = cfg, st
			}
		}
	}
	return c.cfg
}

// setConf takes a config this process has just written.
func (c *Client) setConf(cfg config.Config) {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	c.cfg = cfg
	if c.watchCfg {
		c.cfgStamp = stampOf(config.Path())
	}
}

// Stale reports that the config now asks for another cloud, machine or
// session secrets than the driver was built with — changed by another
// process, or by setup: build a new client.
func (c *Client) Stale() bool {
	return c.watchCfg && driverKey(c.conf()) != c.drvKey
}

// driverKey is everything NewDriver builds from. The images bakes record
// sit in the same sections but go to each create, not the driver.
func driverKey(cfg config.Config) string {
	cfg.AWS.BakedAMI, cfg.AWS.BakedAMIs, cfg.GCP.BakedImages = "", nil, nil
	b, _ := json.Marshal(struct {
		Driver  string
		AWS     config.AWS
		GCP     config.GCP
		Secrets config.Secrets
	}{cfg.Driver, cfg.AWS, cfg.GCP, cfg.Secrets})
	return string(b)
}

// Driver exposes the cloud driver for the few features that manage their
// own processes against it (pier proxy).
func (c *Client) Driver() driver.Driver { return c.drv }

// Cloud names the active cloud for display: "AWS eu-central-1".
func (c *Client) Cloud() string {
	if c.conf().Driver == "gcp-gce" {
		return "GCP " + c.conf().GCP.Zone
	}
	return "AWS " + c.conf().AWS.Region
}

// MachineType is the configured default machine for new sessions.
func (c *Client) MachineType() string {
	if c.conf().Driver == "gcp-gce" {
		return c.conf().GCP.MachineType
	}
	return c.conf().AWS.InstanceType
}

// DiskGiB is the configured per-session disk.
func (c *Client) DiskGiB() int {
	if c.conf().Driver == "gcp-gce" {
		return c.conf().GCP.DiskGiB
	}
	return c.conf().AWS.DiskGiB
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

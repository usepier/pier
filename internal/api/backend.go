package api

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

// Backend is what the server needs from pier: *pier.Client satisfies it,
// and tests substitute a fake.
type Backend interface {
	Cloud() string
	Config() config.Config
	ListSessions(ctx context.Context) ([]pier.Session, error)
	Sweep(ctx context.Context, sessions []pier.Session) []pier.Session
	Enrich(ctx context.Context, sessions []pier.Session) []pier.Session
	ReposFor(sessions []pier.Session, roots []string) []pier.Repo
	Checkouts() []pier.Checkout
	LinkCheckout(dir string) (pier.Checkout, error)
	UnlinkCheckout(root string) error
	Headroom(ctx context.Context) (pier.Quota, error)
	HasKey(s pier.Session) bool
	Resume(ctx context.Context, s pier.Session) error
	Remove(ctx context.Context, s pier.Session) error
	Keep(ctx context.Context, s pier.Session) error
	Park(ctx context.Context, s pier.Session) error
	Resize(ctx context.Context, s pier.Session, machineType string) error
	Machines(s pier.Session) []pier.Machine
	SetPoolSize(ctx context.Context, repoRoot string, n int, progress pier.Progress) (string, error)
	DrainPool(ctx context.Context, repo string, progress pier.Progress) (int, error)
	Settings() []pier.SettingValue
	Set(key, value string) error
	MachineCatalog(currentType string) []pier.Machine
	Doctor(ctx context.Context) []pier.Check
	ReauthCommand(err error) *exec.Cmd
	DiskMonthlyUSD() float64
	StartCreate(req pier.CreateRequest) (pier.Job, error)
	StartBake(repoRoot string, include *bool) (pier.Job, error)
	StartFill(repoRoot string) (pier.Job, error)
	StartResize(s pier.Session, machineType string) (pier.Job, error)
	SetupLog(ctx context.Context, s pier.Session, lines int) (string, error)
	// Close ends the backend's ssh connections.
	Close()
	OpenTerminal(ctx context.Context, s pier.Session, tab, window string, cols, rows int) (*pier.Terminal, error)
	Windows(ctx context.Context, s pier.Session) ([]pier.Window, error)
	NewWindow(ctx context.Context, s pier.Session, name string) (pier.Window, error)
	KillWindow(ctx context.Context, s pier.Session, window string) error
	RenameWindow(ctx context.Context, s pier.Session, window, name string) error
	Follow(ctx context.Context, s pier.Session, topics []string, fn func(ev string, data json.RawMessage)) error
	ContainerAction(ctx context.Context, s pier.Session, container, action string) error
}

var _ Backend = (*pier.Client)(nil)

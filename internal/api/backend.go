package api

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/github"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/gitview"
	"github.com/usepier/pier/pkg/pier/wire"
)

// Backend is what the server needs from pier, by feature: *pier.Client
// satisfies it, and tests substitute a fake.
type Backend interface {
	Account
	Sessions
	Repos
	Terminals
	Chats
	Review
	PullRequests
	Ports
}

// Account is pier's setup on this machine: the cloud, settings, health.
type Account interface {
	Cloud() string
	Config() config.Config
	Settings() []pier.SettingValue
	Set(key, value string) error
	Prompts(repo string) []pier.Prompt
	SetPrompt(repo, key, text string) error
	Doctor(ctx context.Context) []pier.Check
	ReauthCommand(err error) *exec.Cmd
	Headroom(ctx context.Context) (pier.Quota, error)
	MachineCatalog(currentType string) []pier.Machine
	DiskMonthlyUSD() float64
	// Teardown removes pier from the account: pools, images, groundwork.
	Teardown(ctx context.Context, progress pier.Progress) error
	// Close ends the backend's ssh connections.
	Close()
	// Stale: the config now asks for another cloud or machine than this
	// backend was built for (changed by another process) — open a new one.
	Stale() bool
}

// Sessions lists sessions and acts on one.
type Sessions interface {
	ListSessions(ctx context.Context) ([]pier.Session, error)
	Sweep(ctx context.Context, sessions []pier.Session) []pier.Session
	Enrich(ctx context.Context, sessions []pier.Session) []pier.Session
	HasKey(s pier.Session) bool
	Resume(ctx context.Context, s pier.Session) error
	Remove(ctx context.Context, s pier.Session) error
	Keep(ctx context.Context, s pier.Session) error
	Park(ctx context.Context, s pier.Session) error
	Resize(ctx context.Context, s pier.Session, machineType string) error
	Machines(s pier.Session) []pier.Machine
	StartCreate(req pier.CreateRequest) (pier.Job, error)
	StartResize(s pier.Session, machineType string) (pier.Job, error)
	SetupLog(ctx context.Context, s pier.Session, lines int) (string, error)
	// Follow streams a session's topics (status, env, git, chats).
	Follow(ctx context.Context, s pier.Session, topics []string, fn func(ev string, data json.RawMessage)) error
}

// Repos are the repositories pier knows, their checkouts, pools and images.
type Repos interface {
	ReposFor(sessions []pier.Session, roots []string) []pier.Repo
	Checkouts() []pier.Checkout
	LinkCheckout(dir string) (pier.Checkout, error)
	UnlinkCheckout(root string) error
	SetPoolSize(ctx context.Context, repoRoot string, n int, progress pier.Progress) (string, error)
	DrainPool(ctx context.Context, repo string, progress pier.Progress) (int, error)
	StartBake(repoRoot string, include *bool) (pier.Job, error)
	StartFill(repoRoot string) (pier.Job, error)
}

// Terminals are a session's tmux windows and containers.
type Terminals interface {
	OpenTerminal(ctx context.Context, s pier.Session, tab, window string, cols, rows int) (*pier.Terminal, error)
	Windows(ctx context.Context, s pier.Session) ([]pier.Window, error)
	NewWindow(ctx context.Context, s pier.Session, name string) (pier.Window, error)
	KillWindow(ctx context.Context, s pier.Session, window string) error
	RenameWindow(ctx context.Context, s pier.Session, window, name string) error
	ContainerAction(ctx context.Context, s pier.Session, container, action string) error
}

// Chats are a session's conversations with its agents.
type Chats interface {
	FollowChats(ctx context.Context, s pier.Session, topics []string, tails []pier.Tail, fn func(ev string, data json.RawMessage)) error
	CreateChat(ctx context.Context, s pier.Session, r pier.ChatRequest) (string, error)
	SendPrompt(ctx context.Context, s pier.Session, chatID string, p wire.ChatPrompt, mode string) error
	StartDeliver(s pier.Session, d pier.Delivery) (pier.Job, error)
	Dequeue(ctx context.Context, s pier.Session, chatID, promptID string) error
	Interrupt(ctx context.Context, s pier.Session, chatID string) error
	Decide(ctx context.Context, s pier.Session, chatID, requestID string, d pier.Decision) error
	ConfigureChat(ctx context.Context, s pier.Session, chatID string, cfg pier.ChatConfig) error
	CloseChat(ctx context.Context, s pier.Session, chatID string) error
	ChatPage(ctx context.Context, s pier.Session, chatID, before string, limit int) ([]wire.ChatItem, bool, error)
	Models(ctx context.Context, s *pier.Session, agent string) (wire.AgentModels, error)
	ChatWindow(ctx context.Context, s pier.Session, chatID string, takeover bool) (string, error)
	Upload(ctx context.Context, s pier.Session, name string, data []byte) (string, error)
}

// Review is a session's checkout: its changes, files, search, commits.
type Review interface {
	ListFiles(ctx context.Context, s pier.Session) ([]string, bool, error)
	Changes(ctx context.Context, s pier.Session) (gitview.Changes, error)
	FollowGit(ctx context.Context, s pier.Session, fn func(pier.GitState)) error
	KeepSnapshot(ctx context.Context, s pier.Session, next pier.GitState) error
	Diff(ctx context.Context, s pier.Session, f gitview.File, context int) (string, bool, error)
	FileAt(ctx context.Context, s pier.Session, path, at string) (pier.FileContent, error)
	WriteFile(ctx context.Context, s pier.Session, id, path string, data []byte, baseSha string) (string, error)
	Search(ctx context.Context, s pier.Session, q pier.SearchQuery, fn func(ms []pier.Match, done, truncated bool)) error
	Commit(ctx context.Context, s pier.Session, id, message string) (string, error)
	Push(ctx context.Context, s pier.Session, id string) error
}

// PullRequests are sessions' pull requests on GitHub.
type PullRequests interface {
	PullRequests(ctx context.Context, sessions []pier.Session) (map[string]pier.PR, error)
	PullRequest(ctx context.Context, s pier.Session, number int) (pier.PRDetail, error)
	MergePR(ctx context.Context, s pier.Session, prID, expectedHead string) error
	CheckLog(ctx context.Context, s pier.Session, jobID int64, maxBytes int) (string, error)
	GitHubLimit() github.RateLimit
}

// Ports forward sessions' ports to this machine by hand.
type Ports interface {
	ForwardPort(ctx context.Context, s pier.Session, local, remote int) error
	UnforwardPort(s pier.Session, local, remote int) error
}

var _ Backend = (*pier.Client)(nil)

package wire

import (
	"encoding/json"
	"time"
)

// ClientInfo names the client in initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams opens a session with the server.
type InitializeParams struct {
	Protocol int        `json:"protocol"`
	Client   ClientInfo `json:"client"`
	// Approvals says the client shows the agents' permission requests and
	// answers them (chat.decide): while it runs, agents ask it first.
	Approvals bool `json:"approvals,omitempty"`
}

// InitializeResult tells the client what it is talking to.
type InitializeResult struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"` // pier's version
	// Configured is false until pier setup has run on this machine.
	Configured bool   `json:"configured"`
	Cloud      string `json:"cloud"` // "AWS eu-central-1", "" when not configured
}

// CancelParams cancels an in-flight request ($/cancelRequest).
type CancelParams struct {
	ID json.RawMessage `json:"id"`
}

// ProgressParams is one progress event of a long request ($/progress).
type ProgressParams struct {
	ID    json.RawMessage `json:"id"`
	Event Event           `json:"event"`
}

// WatchResult answers a <topic>.watch: the subscription and its first data.
type WatchResult[T any] struct {
	Sub      string `json:"sub"`
	Seq      int    `json:"seq"`
	Snapshot T      `json:"snapshot"`
}

// SubEvent is one update of a subscription (sub.event). Seq increases by one
// per event; a gap means the client missed one and should resubscribe.
type SubEvent struct {
	Sub   string `json:"sub"`
	Seq   int    `json:"seq"`
	Topic string `json:"topic"`
	Data  any    `json:"data"`
}

// SubCancelParams ends a subscription (sub.cancel).
type SubCancelParams struct {
	Sub string `json:"sub"`
}

// ClientActivity tells the server whether anyone is looking
// (client.activity), so it polls the cloud briskly for an open window and
// lazily for a hidden one.
type ClientActivity struct {
	Focused bool `json:"focused"`
	// Visible are the sessions on screen (ids), refreshed first.
	Visible []string `json:"visible,omitempty"`
}

// Empty is a method's absent params or result.
type Empty struct{}

// SessionRef names a session by id.
type SessionRef struct {
	ID string `json:"id"`
}

// ResizeParams changes a session's machine type.
type ResizeParams struct {
	ID          string `json:"id"`
	MachineType string `json:"machineType"`
}

// PathParams names a local path (a checkout or a directory inside one).
type PathParams struct {
	Path string `json:"path"`
}

// RepoRef names a repo by name.
type RepoRef struct {
	Repo string `json:"repo"`
}

// PoolSizeParams sets a repo's pool size; Path is its linked checkout.
type PoolSizeParams struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

// SettingParams sets one setting.
type SettingParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MachinesParams lists machine types around a current one.
type MachinesParams struct {
	Current string `json:"current,omitempty"`
}

// DrainResult says how many pooled sessions a drain removed.
type DrainResult struct {
	Removed int `json:"removed"`
}

// CheckoutInfo describes a local clone the way New Session shows it.
type CheckoutInfo struct {
	Checkout
	Branch string `json:"branch"` // checked-out branch, "" when detached
	Head   string `json:"head"`   // short commit
	// Dirty counts uncommitted changes (tracked edits and untracked files)
	// that travel with a create from HEAD.
	Dirty int    `json:"dirty"`
	Base  string `json:"base"` // origin branch a session from HEAD builds on
}

// TimeOrNil is an optional timestamp: nil for the zero time, UTC otherwise.
func TimeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// CreateParams starts a session from a linked checkout's HEAD, uncommitted
// edits included.
type CreateParams struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"` // default HEAD
	// IdleMs and CapMs override the idle timeout and runaway cap for this
	// session; 0 means never, absent means the setting.
	IdleMs *int64 `json:"idleMs,omitempty"`
	CapMs  *int64 `json:"capMs,omitempty"`
	NoPool bool   `json:"noPool,omitempty"`
	// Agent starts an agent in the session, with its first prompt.
	Agent *AgentParams `json:"agent,omitempty"`
}

// AgentParams is the agent a new session starts with.
type AgentParams struct {
	Kind  string `json:"kind"` // claude | codex
	Model string `json:"model,omitempty"`
	// PermissionMode is claude's --permission-mode; "" keeps its default.
	PermissionMode string `json:"permissionMode,omitempty"`
	// Prompt is the first prompt, typed in once setup has finished.
	Prompt string `json:"prompt,omitempty"`
}

// BakeParams starts a bake of a linked checkout's repo.
type BakeParams struct {
	Path string `json:"path"`
	// IncludeRepo overrides the image setting for this bake: true prebuilds
	// the checkout, false keeps it to toolchains. Absent keeps the setting.
	IncludeRepo *bool `json:"includeRepo,omitempty"`
}

// JobRef names a background job.
type JobRef struct {
	ID string `json:"id"`
}

// LogParams reads the tail of a log.
type LogParams struct {
	ID    string `json:"id"`
	Lines int    `json:"lines,omitempty"` // default 500
}

// LogText is a log's tail.
type LogText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"` // older lines were left out
}

// --- terminals ---------------------------------------------------------

// TermOpenParams opens a terminal tab into a session's tmux.
type TermOpenParams struct {
	SessionID string `json:"sessionId"`
	// Window is the tmux window (@N) the tab shows; "" for main's current.
	Window string `json:"window,omitempty"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

// LocalPurpose names what a local terminal runs. The app never names a
// command itself: pier decides what each purpose runs.
type LocalPurpose string

const (
	LocalLogin LocalPurpose = "login" // the cloud login that fixes an expired session
	LocalSetup LocalPurpose = "setup" // pier setup
	// Logins setup asks for.
	LocalAWSSSO      LocalPurpose = "aws-sso"      // aws sso login --profile <profile>
	LocalGCloudLogin LocalPurpose = "gcloud-login" // gcloud auth login
	LocalGHLogin     LocalPurpose = "gh-login"     // gh auth login
	LocalClaudeToken LocalPurpose = "claude-token" // claude setup-token
	LocalTeardown    LocalPurpose = "teardown"     // pier teardown
)

// TermLocalParams opens a terminal on this machine.
type TermLocalParams struct {
	Purpose LocalPurpose `json:"purpose"`
	// Profile is the AWS profile an aws-sso login is for.
	Profile string `json:"profile,omitempty"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// TermOpened is an open terminal.
type TermOpened struct {
	TermID string `json:"termId"`
}

// TermRef names a terminal.
type TermRef struct {
	TermID string `json:"termId"`
}

// TermData carries terminal bytes, base64-encoded: output in term.output
// (Seq counts up per terminal), keystrokes in term.input.
type TermData struct {
	TermID string `json:"termId"`
	Seq    int    `json:"seq,omitempty"`
	Data   string `json:"data"`
}

// TermSize resizes a terminal (term.resize).
type TermSize struct {
	TermID string `json:"termId"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

// TermAck tells the server how much output the client has rendered
// (term.ack). Output past an unacknowledged window waits, so a terminal
// flooding output can't swamp the app; tmux absorbs the backlog.
type TermAck struct {
	TermID string `json:"termId"`
	Bytes  int    `json:"bytes"`
}

// TermExit says a terminal's process ended (term.exit).
type TermExit struct {
	TermID string `json:"termId"`
	Code   int    `json:"code"`
}

// Window is one tmux window of a session.
type Window struct {
	ID      string `json:"id"` // @N, stable across renames
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Command string `json:"command"` // the active pane's foreground command
	Active  bool   `json:"active"`
	// Role is what pier made the window for: "agent", "setup", or "".
	Role string `json:"role"`
}

// WindowParams names a session's window.
type WindowParams struct {
	SessionID string `json:"sessionId"`
	Window    string `json:"window"`
}

// NewWindowParams adds a window; Name may be empty.
type NewWindowParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name,omitempty"`
}

// RenameWindowParams renames a window.
type RenameWindowParams struct {
	SessionID string `json:"sessionId"`
	Window    string `json:"window"`
	Name      string `json:"name"`
}

// ContainerParams acts on one of a session's containers.
type ContainerParams struct {
	SessionID string `json:"sessionId"`
	Container string `json:"container"`
	Action    string `json:"action"` // start, stop, restart
}

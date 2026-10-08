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

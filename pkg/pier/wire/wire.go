// Package wire holds the data `pier api` speaks: plain structs with JSON
// tags, shared by every frontend that talks to pier over the protocol and
// turned into TypeScript for the Mac app (internal/api/tsgen). It imports
// nothing from pier, so the shapes stay a contract rather than an echo of
// internals.
//
// Conventions: camelCase JSON, times as RFC 3339 strings, durations in
// milliseconds, enums as typed strings whose values are the constants
// declared next to them.
package wire

import "time"

// ProtocolVersion changes whenever a breaking change lands. The Mac app
// bundles its own pier, so a mismatch means a broken install, not a skew to
// negotiate around.
const ProtocolVersion = 1

// SessionState is a session's lifecycle state.
type SessionState string

const (
	StateCreating SessionState = "creating" // provisioning, bootstrapping, or claimed and resuming
	StateRunning  SessionState = "running"  // reachable; a terminal may be attached
	StateWorking  SessionState = "working"  // reachable, an agent is busy
	StateIdle     SessionState = "idle"     // reachable and quiet: the park countdown runs
	StateParked   SessionState = "parked"   // stopped, disk kept
	StateDeleting SessionState = "deleting" // destroy issued, the cloud is still removing it
	StateDead     SessionState = "dead"     // gone outside pier's control
	StateFailed   SessionState = "failed"   // a create that died before the session existed
)

// Session is one session VM, or a pooled one waiting to be claimed.
type Session struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Repo     string       `json:"repo"`             // repo name (the checkout's folder name)
	Origin   string       `json:"origin,omitempty"` // canonical origin remote, host/owner/repo
	Base     string       `json:"base,omitempty"`   // origin branch the session came off
	Branch   string       `json:"branch"`
	State    SessionState `json:"state"`
	Label    string       `json:"label"`    // one word for the row: starting, running, parked, …
	Activity string       `json:"activity"` // what a running session is doing, "" when unknown
	Attached bool         `json:"attached"`
	Strained bool         `json:"strained"` // sustained cpu or memory pressure: a resize hint
	// Setup is the repo setup script's state: "", "running" or "failed".
	Setup       string     `json:"setup"`
	MachineType string     `json:"machineType"`
	CostNote    string     `json:"costNote"`  // "~$0.04/h" running, "~$3-4/mo" parked
	HourlyUSD   float64    `json:"hourlyUsd"` // 0 unless it bills compute right now
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
	Pooled      bool       `json:"pooled"`               // an unclaimed pool member, not a user session
	HasKey      bool       `json:"hasKey"`               // this machine can reach it
	FailReason  string     `json:"failReason,omitempty"` // failed creates only
	LogPath     string     `json:"logPath,omitempty"`    // failed creates: their create log
	// From the supervisor, while running: load, listening ports, and when it
	// stops on its own (parkAt while quiet and detached, capAt while it
	// works unattended).
	Resources *Resources `json:"resources,omitempty"`
	Ports     []Port     `json:"ports,omitempty"`
	ParkAt    *time.Time `json:"parkAt,omitempty"`
	CapAt     *time.Time `json:"capAt,omitempty"`
}

// Resources is a session's load. Sizes are bytes.
type Resources struct {
	CPU       float64 `json:"cpu"` // % of all CPUs
	CPUs      int     `json:"cpus"`
	MemUsed   uint64  `json:"memUsed"`
	MemTotal  uint64  `json:"memTotal"`
	DiskUsed  uint64  `json:"diskUsed"`
	DiskTotal uint64  `json:"diskTotal"`
	Load1     float64 `json:"load1"`
}

// Port is a port a session listens on and who holds it.
type Port struct {
	Port    int    `json:"port"`
	Process string `json:"process"`
	PID     int    `json:"pid,omitempty"`
}

// Checkout is a linked local clone the app creates, bakes and fills from.
type Checkout struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Origin  string `json:"origin,omitempty"`
	Missing bool   `json:"missing"` // the clone is gone from disk
}

// ImageKind says what a repo's session image holds.
type ImageKind string

const (
	ImageNone      ImageKind = ""          // no image: sessions run the full setup
	ImagePrebuilt  ImageKind = "prebuilt"  // the checkout with setup already run
	ImageToolchain ImageKind = "toolchain" // toolchains only
	ImageUnknown   ImageKind = "unknown"   // baked before pier recorded how
)

// Repo is one repository's row: its image, pool, sessions and idle cost.
type Repo struct {
	Name      string     `json:"name"`
	Origin    string     `json:"origin,omitempty"`
	Checkout  *Checkout  `json:"checkout,omitempty"` // the linked clone actions run from
	HasPier   bool       `json:"hasPier"`            // the checkout carries a .pier/ directory
	Image     string     `json:"image,omitempty"`
	ImageKind ImageKind  `json:"imageKind"`
	BakedAt   *time.Time `json:"bakedAt,omitempty"`
	Baking    bool       `json:"baking"` // a bake started on this machine is still running
	// The pool: Target members kept, Ready parked and claimable, Filling
	// being set up, Stale about to recycle. Ages is one entry per member.
	PoolTarget  int      `json:"poolTarget"`
	PoolReady   int      `json:"poolReady"`
	PoolFilling int      `json:"poolFilling"`
	PoolStale   int      `json:"poolStale"`
	PoolAges    []string `json:"poolAges"`
	Sessions    int      `json:"sessions"`
	MonthlyUSD  float64  `json:"monthlyUsd"` // pooled disks plus image storage
	// HourlyUSD is what the repo's sessions bill right now; IdleMonthlyUSD
	// what it costs while nothing runs: parked disks, the pool, the image.
	HourlyUSD      float64    `json:"hourlyUsd"`
	IdleMonthlyUSD float64    `json:"idleMonthlyUsd"`
	Reminders      []Reminder `json:"reminders"`
}

// Reminder is one piece of rebake guidance. pier never acts on it.
type Reminder struct {
	Message string `json:"message"`
	Action  string `json:"action"`
}

// Totals are the account-wide numbers the header shows.
type Totals struct {
	Running        int     `json:"running"` // includes starting
	Parked         int     `json:"parked"`
	PoolReady      int     `json:"poolReady"`
	HourlyUSD      float64 `json:"hourlyUsd"`      // compute billing right now
	IdleMonthlyUSD float64 `json:"idleMonthlyUsd"` // parked disks, pools and images
}

// Quota is the account's vCPU headroom in the active region.
type Quota struct {
	Used   int    `json:"used"`
	Limit  int    `json:"limit"`
	Detail string `json:"detail"`
}

// Overview is everything the sidebar, Home and the cockpits show, refreshed
// as a whole.
type Overview struct {
	Cloud     string     `json:"cloud"`
	Sessions  []Session  `json:"sessions"` // pooled members included (Pooled)
	Repos     []Repo     `json:"repos"`
	Checkouts []Checkout `json:"checkouts"`
	Totals    Totals     `json:"totals"`
	Quota     *Quota     `json:"quota,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
	// Enriched says the running sessions' statuses were read this round;
	// false rows carry the previous reading.
	Enriched bool `json:"enriched"`
	// Error is why the last refresh failed; the data is the last good one.
	Error *ErrorData `json:"error,omitempty"`
}

// Machine is one machine type a session can run as.
type Machine struct {
	Type string `json:"type"`
	CPU  string `json:"cpu"` // vCPUs
	Mem  string `json:"mem"` // GiB
	Cost string `json:"cost"`
}

// SettingKind is how a setting is edited.
type SettingKind string

const (
	SettingText    SettingKind = "text"
	SettingChoice  SettingKind = "choice"
	SettingMachine SettingKind = "machine" // pick from the cloud's machine catalog
)

// SettingOption is one choice of a choice setting.
type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Desc  string `json:"desc,omitempty"`
}

// SettingGroup is a settings page section, in page order.
type SettingGroup struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// Setting is one settings row: its schema and current value.
type Setting struct {
	Key      string          `json:"key"`
	Group    string          `json:"group"`
	Label    string          `json:"label"`
	Hint     string          `json:"hint"`
	Detail   string          `json:"detail"` // multi-line explanation; `code` spans in backticks
	Default  string          `json:"default"`
	Kind     SettingKind     `json:"kind"`
	Options  []SettingOption `json:"options,omitempty"`
	NoCustom bool            `json:"noCustom"`
	Empty    string          `json:"empty,omitempty"`  // what an empty value reads as
	Suffix   string          `json:"suffix,omitempty"` // unit shown after the value
	Value    string          `json:"value"`
	Display  string          `json:"display"`
}

// Settings is the whole settings page.
type Settings struct {
	Groups   []SettingGroup `json:"groups"`
	Settings []Setting      `json:"settings"`
}

// CheckStatus is a doctor check's outcome.
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckFail CheckStatus = "fail"
)

// Check is one doctor check.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

// EventKind is the kind of a progress event.
type EventKind string

const (
	EventStep   EventKind = "step"   // a milestone
	EventNote   EventKind = "note"   // detail worth showing
	EventWarn   EventKind = "warn"   // something went sideways but work continues
	EventDone   EventKind = "done"   // a job's last event: it finished
	EventFailed EventKind = "failed" // a job's last event: it failed, Message says why
)

// Event is one progress line of a long operation.
type Event struct {
	Kind      EventKind `json:"kind"`
	Message   string    `json:"message"`
	ElapsedMs int64     `json:"elapsedMs"`
}

// ErrorKind classifies a failed call so the app can react without parsing
// messages.
type ErrorKind string

const (
	ErrParse          ErrorKind = "parse"
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrNoMethod       ErrorKind = "method_not_found"
	ErrInvalidParams  ErrorKind = "invalid_params"
	ErrInternal       ErrorKind = "internal"
	ErrFailed         ErrorKind = "failed" // the operation itself failed; message says why
	ErrCancelled      ErrorKind = "cancelled"
	ErrNotSetUp       ErrorKind = "not_set_up"   // run setup first
	ErrAuthExpired    ErrorKind = "auth_expired" // Login is the command that fixes it
	ErrNotFound       ErrorKind = "not_found"
	ErrAmbiguous      ErrorKind = "ambiguous"
	ErrStillCreating  ErrorKind = "still_creating"
	ErrCreateFailed   ErrorKind = "create_failed"
	ErrDeleting       ErrorKind = "deleting"
	ErrParked         ErrorKind = "parked"
	ErrNoKey          ErrorKind = "no_key" // the session's key lives on another machine
	ErrNotInRepo      ErrorKind = "not_in_repo"
	ErrUnreachable    ErrorKind = "unreachable"
	ErrConflict       ErrorKind = "conflict"   // a file changed underneath the write
	ErrHeadMoved      ErrorKind = "head_moved" // the pull request moved past what was reviewed
	ErrBusy           ErrorKind = "busy"
)

// ErrorData rides in a JSON-RPC error's data field.
type ErrorData struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
	// Login is the interactive login that fixes an auth_expired error.
	Login []string `json:"login,omitempty"`
}

// JobKind names what a background job does.
type JobKind string

const (
	JobCreate JobKind = "create"
	JobBake   JobKind = "bake"
	JobFill   JobKind = "fill"
	JobResize JobKind = "resize"
)

// JobState is where a background job is.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
	JobLost    JobState = "lost" // its process ended without reporting how
)

// Job is long work running as its own process, so it finishes even if the
// app quits: a create, a bake, a pool fill, a resize.
type Job struct {
	ID        string     `json:"id"`
	Kind      JobKind    `json:"kind"`
	Repo      string     `json:"repo,omitempty"`
	Session   string     `json:"session,omitempty"`
	State     JobState   `json:"state"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Log       string     `json:"log"` // the human log file
}

// JobUpdate is a job's state with its progress events: all of them in a
// watch's snapshot, the new ones in each update.
type JobUpdate struct {
	Job    Job     `json:"job"`
	Events []Event `json:"events"`
}

// Env is what runs in a session: its tmux windows, docker containers,
// heaviest processes and listening ports, live.
type Env struct {
	Windows    []Window    `json:"windows"`
	Containers []Container `json:"containers"`
	Processes  []Process   `json:"processes"`
	Ports      []Port      `json:"ports"`
	Resources  *Resources  `json:"resources,omitempty"`
	At         time.Time   `json:"at"`
}

// Container is one docker container in a session.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`  // running, exited, …
	Status string `json:"status"` // docker's words: "Up 2 hours"
	Ports  string `json:"ports"`
}

// Process is one of a session's heaviest processes.
type Process struct {
	PID     int     `json:"pid"`
	Command string  `json:"command"`
	CPU     float64 `json:"cpu"` // % of one CPU
	RSS     uint64  `json:"rss"` // bytes
}

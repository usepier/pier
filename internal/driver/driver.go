// Package driver defines the contract every cloud driver implements.
//
// Design invariants (see docs/SPEC.md):
//   - No cloud credentials ever live inside a session VM. Self-park is the VM
//     shutting itself down; everything else is client-initiated.
//   - Session state is derived from provider APIs + tags/labels. No database,
//     no server, no background process on the laptop.
//   - The caller's cloud identity (STS ARN / GCP principal) namespaces every
//     resource, so many devs share one account without collisions.
package driver

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

type State string

const (
	StateCreating State = "creating" // provisioning / first boot
	StateRunning  State = "running"  // reachable, a client is attached
	StateWorking  State = "working"  // reachable, detached, agent actively working
	StateIdle     State = "idle"     // reachable, quiet; park countdown running
	StateParked   State = "parked"   // instance stopped, disk persists
	StateDeleting State = "deleting" // destroy issued; the cloud is still removing it
	StateDead     State = "dead"     // terminated/crashed outside our control
	// StateFailed is a local tombstone, not a machine: the create died before
	// the session existed and took its half-made instance with it. The row
	// survives so a failed create leaves a trace instead of a silent gap in
	// the list; `pier rm` (d in the TUI) dismisses it.
	StateFailed State = "failed"
)

// Session is one instance + its persistent disk.
type Session struct {
	ID     string // provider instance ID
	Name   string // branch-derived session name
	Repo   string
	Branch string
	// Origin is the repo's origin remote, canonical host/path (no
	// credentials); "" for sessions made before pier recorded it, or from
	// repos without one. Base is the origin branch the session came off.
	Origin       string
	Base         string
	User         string // derived from cloud caller identity, never configured
	Driver       string
	State        State
	Strained     bool      // sustained cpu/mem pressure (supervisor beacon) — resize hint
	Attached     bool      // a terminal is attached right now (supervisor beacon; false when unknown)
	Setup        string    // repo setup script: "" | "running" | "failed" (supervisor beacon)
	InstanceType string    // provider machine type; feeds the TUI resize picker
	Created      time.Time // session creation, not last boot — AGE must never go backward
	CostNote     string    // honest money: "~$3-4/mo" parked, the type's hourly rate otherwise
	PoolGen      string    // warm-pool generation fingerprint; non-empty = unclaimed pool member, not a user session
	// FailReason is why a StateFailed tombstone exists — the create error,
	// verbatim. Empty for every real session.
	FailReason string
	// LogPath points a tombstone at its create log. Empty when the create ran
	// in the foreground and its output went to the terminal instead.
	LogPath string

	// From the supervisor's beacon, for running sessions (zero otherwise):
	// its load, who listens on which port, and when it parks — ParkAt while
	// quiet and detached, CapAt while it works with nobody attached.
	Res    *Resources
	Ports  []Port
	ParkAt time.Time
	CapAt  time.Time
	// Chats are the session's agent chats, as its supervisor last said.
	Chats []wire.ChatSummary
}

// Resources is a session's load. Sizes are bytes; CPU is % of all CPUs.
type Resources struct {
	CPU       float64
	CPUs      int
	MemUsed   uint64
	MemTotal  uint64
	DiskUsed  uint64
	DiskTotal uint64
	Load1     float64
}

// Port is a port a session listens on and the process holding it.
type Port struct {
	Port    int
	Process string
	PID     int
}

// Machine is one row in the TUI's resize picker: a curated instance type
// with its shape and rough on-demand cost, so nobody memorizes type names.
type Machine struct {
	Type string
	CPU  string // vCPUs
	Mem  string // GiB
	Cost string
}

// CreateSpec describes a new session. Create returns as soon as the instance
// is attach-ready; code push and .pier/setup.sh continue asynchronously in
// the session (the async create pipeline) with steps streamed via Progress.
type CreateSpec struct {
	Name          string
	Repo          string        // local repo root; the bundle source
	Branch        string        // new branch, off BaseRef
	BaseRef       string        // default: HEAD
	Image         string        // the repo's baked image ID; "" = stock (guarded cloud-init installs everything)
	IdleTimeout   time.Duration // 0 = never auto-park
	UnattendedCap time.Duration // 0 = no runaway cap
	PoolGen       string        // non-empty: create a warm pool member (drivers add the pool tag/label; Name is the placeholder)
	Chat          *ChatSpec     // the chat to start the session with; nil = none
	Progress      func(step string)
}

// ChatSpec is the chat a session starts with: its agent, how it runs,
// and the prompt it begins on (delivered once the repo's setup is done).
type ChatSpec struct {
	ID     string
	Agent  string // claude | codex
	Model  string
	Effort string
	Fast   bool
	Mode   string // permission mode
	Prompt string
	// PromptID makes the prompt's delivery happen once.
	PromptID string
	// NameBranch is the session's stand-in branch, to be named after the
	// prompt in the session (and the session relabeled after it).
	NameBranch string
}

// ClaimWindow bounds how long a claimed-but-not-ready session lists as
// starting. A claim that died before MarkReady (the laptop went away) must
// not leave a parked session reading "starting" forever.
const ClaimWindow = 15 * time.Minute

// ErrNoKey: this machine holds no ssh key for the session. Each session's
// key lives only on the laptop that created it, so another machine (or a
// cleared ~/.config/pier/keys) can't reach it — and retrying won't help.
var ErrNoKey = errors.New("no ssh key for this session on this machine")

// ErrClaimLost: another claimer won this pool member — move on to the next
// candidate.
var ErrClaimLost = errors.New("pool member claimed by another process")

// LoginError is a cloud CLI failure an interactive login fixes: the CLI's
// login session expired or was never made. Login is the argv of that login,
// for a frontend to run in the user's terminal; Err keeps the CLI's message.
type LoginError struct {
	Login []string
	Err   error
}

func (e *LoginError) Error() string { return e.Err.Error() }
func (e *LoginError) Unwrap() error { return e.Err }

// ErrQuota: a cloud quota (vCPUs, instances) refused the request; the
// quota needs raising.
var ErrQuota = errors.New("a cloud quota refused the request")

// CloudError is a cloud API's refusal as its CLI reports it: the code the
// API gave (VcpuLimitExceeded, NoSuchEntity, QUOTA_EXCEEDED) and the call
// it refused. Err keeps the whole failure, with the command.
type CloudError struct {
	Code    string
	Op      string
	Message string
	Err     error
}

func (e *CloudError) Error() string { return e.Err.Error() }
func (e *CloudError) Unwrap() error { return e.Err }

// Is makes a quota refusal ErrQuota.
func (e *CloudError) Is(target error) bool {
	if target != ErrQuota {
		return false
	}
	switch e.Code {
	case "VcpuLimitExceeded", "InstanceLimitExceeded", "MaxSpotInstanceCountExceeded", "QUOTA_EXCEEDED":
		return true
	}
	return false
}

// ErrorCode is the cloud API's code for err ("" when it carries none).
func ErrorCode(err error) string {
	var ce *CloudError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// ClaimSpec names the session a warm pool member becomes.
type ClaimSpec struct {
	Name   string
	Branch string
	Origin string // canonical origin of the claiming repo (payload.RepoOrigin)
	Base   string // origin branch the session comes off (payload.BaseBranch)
	// Nonce is this claimer's unique token. Tags and labels have no
	// compare-and-swap, so Claim writes the nonce, waits a settle beat, and
	// reads it back: of two concurrent claimers the later write wins and the
	// earlier one sees a foreign nonce (ErrClaimLost). This closes the
	// same-user two-terminal race — it is not a security boundary; per-user
	// namespacing is.
	Nonce string
}

// BakeSpec describes one repo's image bake. Images are repo-specific: the
// default install serves pier and the harnesses; whatever a repo's toolchain
// needs on top (pnpm, python, ...) comes from its .pier/bake.sh; and with
// RepoRoot set, the bake is a prebuild — the repo checked out and its
// .pier/setup.sh run to completion, so sessions start from the result.
type BakeSpec struct {
	RepoName string // repo basename; keys the image to its repo
	RepoRoot string // local repo root to prebuild from; "" = toolchains only
	BaseRef  string // the commit to prebuild ("" = HEAD): origin's default branch
	HookPath string // local path to the repo's .pier/bake.sh (as BaseRef has it); "" = none
	Progress func(step string)
}

// Baked is a bake whose image is being saved. Saving copies the bake disk
// into image storage and can outlast the bake itself, so Bake returns as soon
// as it starts; Instance, the stopped bake instance, is held until
// FinishBake sees the image through.
type Baked struct {
	Image    string
	Instance string
}

// Image is one of the caller's baked session images, as the cloud lists it.
// The cloud is the record: the config only caches which image a repo uses,
// so a lost config never orphans a still-billing image.
type Image struct {
	ID      string
	Repo    string // the repo name the bake tagged it with (payload.Sanitize form)
	Created time.Time
}

// Step reports one bake phase; a nil Progress discards it.
func (s BakeSpec) Step(msg string) {
	if s.Progress != nil {
		s.Progress(msg)
	}
}

type Check struct {
	Name   string
	OK     bool
	Detail string
	// Fix is the command that fixes a failed check, when one does.
	Fix string
}

type SetupReport struct {
	Created []string // resources created this run
	Existed []string // groundwork found and adopted (second-dev path)
}

type Quota struct {
	Used   int
	Limit  int
	Detail string
}

// Driver is implemented by awsec2 and gcpgce (later: k8s, fly, docker).
type Driver interface {
	Name() string

	// SetupOnce creates the per-account groundwork (idempotent, tiny: on AWS
	// an instance role + egress-only SG; on GCP two API enables + one IAP
	// firewall rule). Second devs find everything in Existed.
	SetupOnce(ctx context.Context) (SetupReport, error)
	Teardown(ctx context.Context) error
	Doctor(ctx context.Context) []Check

	Create(ctx context.Context, spec CreateSpec) (*Session, error)
	Resume(ctx context.Context, id string) error
	Park(ctx context.Context, id string) error // client-initiated; normal parking is the VM's own shutdown
	Destroy(ctx context.Context, id string) error

	// Claim converts a warm pool member into a named session: nonce
	// write + read-back (see ClaimSpec.Nonce), then name/branch rewrite;
	// clearing the pool marker is the commit point, after which List reports
	// a regular session. Tags/labels only — the caller Resumes and freshens
	// the instance itself.
	Claim(ctx context.Context, id string, spec ClaimSpec) error

	// Supervisor returns the in-VM supervisor build for instances of this
	// type (its CPU architecture): a claim installs it on the member.
	Supervisor(ctx context.Context, instanceType string) ([]byte, error)

	// Rename relabels a session — its name and branch — after the session
	// renamed its branch itself.
	Rename(ctx context.Context, id, name, branch string) error

	// MarkReady writes the ready tag/label: the session is attachable. It's
	// a create's last act, and a claim's — Claim clears it at its commit
	// point, so a claimed session lists as starting, not parked, while it
	// resumes and freshens.
	MarkReady(ctx context.Context, id string) error

	// Resize changes the instance size (vertical scaling). Providers only
	// allow this on stopped instances, and park is exactly a stop — so a
	// running session rides one park+resume cycle (~40s down); a parked one
	// stays parked. Same CPU architecture only: the disk's binaries live on.
	Resize(ctx context.Context, id, instanceType string) error

	// List returns only the caller's sessions (identity-filtered tags/labels).
	List(ctx context.Context) ([]Session, error)

	// Machines is the curated resize-picker catalog: same-architecture types
	// compatible with currentType, with shape and rough cost. nil means no
	// catalog — `pier resize <session> <type>` still takes any type.
	Machines(currentType string) []Machine

	// AttachCommand hands the terminal over: `aws ssm start-session` via
	// session-manager-plugin, or `gcloud compute ssh --tunnel-through-iap`.
	AttachCommand(ctx context.Context, id string) (*exec.Cmd, error)

	// PortForwardCommand holds local→session port forwards open until the
	// process is interrupted: the app in the session on your laptop's
	// browser, its database in your local psql. pairs are {local, remote}.
	PortForwardCommand(ctx context.Context, id string, pairs [][2]int) (*exec.Cmd, error)

	// SSHTarget exposes the raw OpenSSH recipe for a session — option args
	// and destination — for features that manage their own ssh processes
	// (pier proxy's multiplexed masters and live-adjusted forwards). Pier's
	// one remote mechanism is OpenSSH, so this is a contract, not a leak.
	SSHTarget(ctx context.Context, id string) (opts []string, dest string, err error)

	// InvalidateTarget forgets anything SSHTarget cached about reaching a
	// session (a probed public IP), so the next connection resolves afresh
	// — after a failure, or once the session parked and woke elsewhere.
	InvalidateTarget(id string)

	// Exec runs a one-shot command (status reads, push bootstrap) without a TTY.
	Exec(ctx context.Context, id string, command string) (string, error)

	// Bake builds one repo's prebaked session image: harnesses, the repo's
	// .pier/bake.sh toolchains, and (prebuild) its checkout with
	// .pier/setup.sh already run, so creates skip the repo's own setup cost.
	// It returns once the image is being saved; on error nothing is left.
	Bake(ctx context.Context, spec BakeSpec) (Baked, error)
	// FinishBake waits until the bake's image can launch sessions, then
	// removes the bake instance. An image that failed is deleted as well; a
	// cancelled wait leaves both for the next bake, image reconcile or sweep.
	FinishBake(ctx context.Context, b Baked, step func(string)) error

	// Images lists the caller's available baked images: tagged with the
	// caller's identity, or with none (bakes from before images carried one).
	// Images still being created are left out.
	Images(ctx context.Context) ([]Image, error)
	// DeleteImage removes an image and its storage (snapshots).
	DeleteImage(ctx context.Context, id string) error

	// Headroom reports account capacity (vCPU quota) for the create-time
	// opportunistic check and the TUI header.
	Headroom(ctx context.Context) (Quota, error)
}

// BakeName is the name and repo a bake's temporary machine is listed under.
const BakeName = "bake"

// IsBakeMachine reports whether s is the temporary machine a bake runs on:
// listed like a session, but no one's — frontends and the proxy leave it
// out; the sweep doesn't.
func IsBakeMachine(s Session) bool {
	return s.Name == BakeName && s.Repo == BakeName && s.PoolGen == ""
}

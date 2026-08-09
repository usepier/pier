// Package gcpgce implements the pier driver on GCE + Persistent Disk.
//
// Shape (settled in docs/SPEC.md, timings from spike/gcp.sh):
//   - Session = one GCE instance (default e2-medium, Ubuntu 24.04) with a
//     pd-balanced root disk (default 40GB). Instance stop/start = park/resume;
//     an in-VM `shutdown -h now` lands in TERMINATED with the disk intact.
//   - Networking: default network, ephemeral external IP for egress only.
//     Two firewall rules target-tagged to pier VMs: allow Google's IAP range
//     (35.235.240.0/20) -> :22 at priority 999, deny all other ingress at
//     1000. The deny outranks any permissive rule the shared network carries
//     (default networks ship default-allow-ssh open to the world), so
//     nothing but the IAP tunnel reaches the instance.
//   - Instances run with NO service account and no scopes: zero cloud
//     permissions inside the VM, ever.
//   - Attach and every other remote op is raw OpenSSH through a
//     start-iap-tunnel ProxyCommand (see cli.go — never `gcloud compute ssh`).
//   - Identity: the active gcloud account. Its label-safe form namespaces the
//     pier-user label List filters on; the full principal rides instance
//     metadata and is verified client-side.
//   - The instance name IS the session ID (GCE has no separate id worth
//     carrying). Names are unique per project+zone, so a short principal hash
//     suffixes them — two devs can both have a "fix-auth" session.
//   - Filterable state lives in labels (charset [a-z0-9_-]); freeform values
//     (session name, branch, repo) live in instance metadata.
package gcpgce

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/payload"
)

const (
	FirewallRule  = "pier-allow-iap-ssh"
	FirewallDeny  = "pier-deny-ingress"
	IAPRange      = "35.235.240.0/20"
	NetworkTag    = "pier-session"
	LabelManaged  = "pier-managed"
	LabelUser     = "pier-user"
	LabelReady    = "pier-ready"    // create's last act: bootstrap done, attachable
	LabelDeleting = "pier-deleting" // destroy's first act: the delete itself takes a minute+
	MetaSession   = "pier-session"
	MetaRepo      = "pier-repo"
	MetaBranch    = "pier-branch"
	MetaUser      = "pier-user" // full principal; the label form is lossy

	stockImageProject = "ubuntu-os-cloud"
	stockImageFamily  = "ubuntu-2404-lts-%s" // amd64 | arm64
)

type Driver struct {
	Project     string
	Zone        string
	MachineType string
	DiskGiB     int

	// StateDir holds per-session ssh keys + known_hosts (the config dir).
	StateDir string
	// Manifest: $HOME-relative files/dirs copied into each session.
	Manifest []string
	// SessionEnv is written to ~/.config/pier/env in the session (sourced by
	// bashrc): GH_TOKEN, CLAUDE_CODE_OAUTH_TOKEN, ...
	SessionEnv map[string]string
	// SupervisorBin returns the embedded pier-supervisor binary for an arch
	// ("arm64"/"amd64").
	SupervisorBin func(arch string) ([]byte, error)

	principal string // cached
}

var _ driver.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return "gcp-gce" }

// instanceName derives the session's instance name (= its ID): sanitized
// session name plus a short principal hash, so two devs sharing a project
// can hold the same session name without colliding on GCE's per-zone name
// uniqueness. Budgeted under the 63-char instance-name limit.
func instanceName(session, principal string) string {
	h := fnv.New32a()
	h.Write([]byte(principal))
	s := payload.Sanitize(session)
	if len(s) > 51 {
		s = strings.TrimRight(s[:51], "-")
	}
	return fmt.Sprintf("pier-%s-%06x", s, h.Sum32()&0xffffff)
}

// labelValue folds a freeform value (the principal) into GCE's label charset
// [a-z0-9_-]. Lossy — which is why the full value also rides metadata.
func labelValue(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-_")
	}
	return out
}

// archOf maps a machine type to arm64/amd64 (selects image + supervisor
// build). GCE's arm families are t2a and c4a; everything else here is x86.
func archOf(machineType string) string {
	family, _, _ := strings.Cut(machineType, "-")
	if family == "t2a" || family == "c4a" {
		return "arm64"
	}
	return "amd64"
}

// regionOf strips the zone suffix: europe-west3-a -> europe-west3.
func regionOf(zone string) string {
	if i := strings.LastIndex(zone, "-"); i > 0 {
		return zone[:i]
	}
	return zone
}

type gceInstance struct {
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	CreationTimestamp string            `json:"creationTimestamp"`
	MachineType       string            `json:"machineType"` // full URL; base is the type
	Labels            map[string]string `json:"labels"`
	Metadata          struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	} `json:"metadata"`
}

func (d *Driver) List(ctx context.Context) ([]driver.Session, error) {
	me, err := d.user(ctx)
	if err != nil {
		return nil, err
	}
	out, err := d.gcloud(ctx, "compute", "instances", "list",
		"--zones", d.Zone,
		"--filter", "labels."+LabelManaged+"=1 AND labels."+LabelUser+"="+labelValue(me),
		"--format", "json(name,status,creationTimestamp,machineType,labels,metadata)")
	if err != nil {
		return nil, err
	}
	var raw []gceInstance
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	var sessions []driver.Session
	for _, in := range raw {
		s := driver.Session{ID: in.Name, User: me, Driver: d.Name(),
			InstanceType: path.Base(in.MachineType)}
		owner := ""
		for _, m := range in.Metadata.Items {
			switch m.Key {
			case MetaSession:
				s.Name = m.Value
			case MetaRepo:
				s.Repo = m.Value
			case MetaBranch:
				s.Branch = m.Value
			case MetaUser:
				owner = m.Value
			}
		}
		// The label filter is lossy (folded charset); the metadata principal
		// is exact. A fold collision must not leak someone else's session.
		if owner != me {
			continue
		}
		if in.Labels[LabelDeleting] == "1" {
			// A GCE delete takes a minute+, and mid-delete the status reads
			// STOPPING/TERMINATED — indistinguishable from parking. Destroy
			// labels first so the list can tell going-away from waiting.
			s.State = driver.StateDeleting
		} else {
			switch in.Status {
			case "PROVISIONING", "STAGING":
				s.State = driver.StateCreating
			case "RUNNING":
				// GCE says RUNNING long before the session is usable. The ready
				// label is the create's last act, so its absence means
				// still-creating — a truthful state with no probe.
				s.State = driver.StateRunning // enriched to working/idle by the caller
				if in.Labels[LabelReady] != "1" {
					s.State = driver.StateCreating
				}
			case "STOPPING", "SUSPENDING", "SUSPENDED", "TERMINATED":
				s.State = driver.StateParked
			default:
				s.State = driver.StateDead
			}
		}
		// creationTimestamp survives stop/start (unlike EC2 launch time), so
		// AGE never goes backward and no created tag is needed.
		if ts, err := time.Parse(time.RFC3339, in.CreationTimestamp); err == nil {
			s.Created = ts
		}
		s.CostNote = costNote(s.State, s.InstanceType)
		sessions = append(sessions, s)
	}
	slices.SortFunc(sessions, func(a, b driver.Session) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sessions, nil
}

// costNote reads the hourly rate from the same catalog the resize picker
// shows, so the list and the picker can never quote different prices for
// one machine. Types outside the catalog show nothing — blank beats wrong.
func costNote(st driver.State, machineType string) string {
	switch st {
	case driver.StateParked:
		return "~$4/mo"
	case driver.StateDeleting, driver.StateDead:
		return ""
	}
	for _, m := range Machines(machineType) {
		if m.Type == machineType {
			return m.Cost
		}
	}
	return ""
}

func (d *Driver) Resume(ctx context.Context, id string) error {
	if _, err := d.gcloud(ctx, "compute", "instances", "start", id, "--zone", d.Zone); err != nil {
		return err
	}
	return d.waitSSH(ctx, id, 300*time.Second)
}

// Park stops async: the instance takes ~60s to reach TERMINATED, and nothing
// pier does next depends on it getting there.
func (d *Driver) Park(ctx context.Context, id string) error {
	_, err := d.gcloud(ctx, "compute", "instances", "stop", id, "--zone", d.Zone, "--async")
	return err
}

// Resize: GCE permits machine-type changes only while TERMINATED, and park is
// exactly a stop — so resize is park → set-machine-type → resume. A parked
// session is resized in place and stays parked (costs nothing to leave it
// that way). Verified by spike/gcp.sh.
func (d *Driver) Resize(ctx context.Context, id, machineType string) error {
	out, err := d.gcloud(ctx, "compute", "instances", "describe", id, "--zone", d.Zone,
		"--format", "value(status,machineType.basename())")
	if err != nil {
		return err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return fmt.Errorf("unexpected describe output: %q", out)
	}
	state, curType := f[0], f[1]
	if curType == machineType {
		return fmt.Errorf("session is already a %s", machineType)
	}
	if archOf(machineType) != archOf(curType) {
		return fmt.Errorf("cannot resize across architectures: the disk is %s, %s is %s — pick a same-arch type",
			archOf(curType), machineType, archOf(machineType))
	}

	wasRunning := false
	switch state {
	case "RUNNING", "PROVISIONING", "STAGING":
		wasRunning = true
		// Synchronous stop: set-machine-type needs TERMINATED, not STOPPING.
		if _, err := d.gcloud(ctx, "compute", "instances", "stop", id, "--zone", d.Zone); err != nil {
			return err
		}
	case "STOPPING":
		// on its way down; wait for TERMINATED by stopping again (idempotent)
		if _, err := d.gcloud(ctx, "compute", "instances", "stop", id, "--zone", d.Zone); err != nil {
			return err
		}
	case "TERMINATED":
		// already parked; resize in place
	default:
		return fmt.Errorf("session is %s — nothing to resize", strings.ToLower(state))
	}
	if _, err := d.gcloud(ctx, "compute", "instances", "set-machine-type", id,
		"--zone", d.Zone, "--machine-type", machineType); err != nil {
		return err
	}
	if wasRunning {
		return d.Resume(ctx, id)
	}
	return nil
}

func (d *Driver) Destroy(ctx context.Context, id string) error {
	// Label first, best-effort: the delete below takes a minute+ and a
	// mid-delete instance otherwise lists as parked. If the delete then
	// fails, the row shows deleting and a retry re-runs both calls.
	_, _ = d.gcloud(ctx, "compute", "instances", "add-labels", id,
		"--zone", d.Zone, "--labels", LabelDeleting+"=1")
	// The boot disk auto-deletes with the instance (create-time default).
	if _, err := d.gcloud(ctx, "compute", "instances", "delete", id, "--zone", d.Zone); err != nil {
		return err
	}
	_ = os.Remove(d.keyPath(id))
	_ = os.Remove(d.keyPath(id) + ".pub")
	// Drop the host key: a same-named recreate (same session name, same
	// principal) would otherwise trip a known_hosts mismatch.
	hk := exec.Command("ssh-keygen", "-R", id, "-f", d.StateDir+"/known_hosts")
	hk.Stdout, hk.Stderr = nil, nil
	_ = hk.Run()
	return nil
}

// AttachCommand forwards the laptop's ssh agent (a no-op when none runs) and
// refreshes ~/.ssh/agent.sock before tmux: each attach gets a fresh forwarded
// socket path, while long-lived tmux panes hold the old one — bashrc points
// them at the symlink instead, so `git push` over ssh keeps working across
// re-attaches. Keys never leave the laptop; detached sessions can't use them.
//
// It also refuses on a missing bootstrap marker: attaching mid-create would
// land in an empty $HOME (no repo yet) and steal the `main` tmux session
// away from its workdir. The ready label stops pier's own commands well
// before this, so it's a backstop for races and raw ssh users.
func (d *Driver) AttachCommand(ctx context.Context, id string) (*exec.Cmd, error) {
	const remote = `[ -S "$SSH_AUTH_SOCK" ] && ln -sf "$SSH_AUTH_SOCK" ~/.ssh/agent.sock
[ -e "$HOME/.pier-bootstrapped" ] || { echo "pier: this session is still setting up — attach again when it shows running in pier ls" >&2; exit 1; }
exec tmux new-session -A -s main`
	args := append(d.sshOpts(id), "-t", "-o", "ForwardAgent=yes", "agent@"+id, remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd, nil
}

// MCPLoginCommand: interactive `claude mcp login` with the OAuth callback
// port forwarded through the IAP tunnel. The auth URL prints in the user's
// terminal (BROWSER=echo keeps headless claude from skipping straight to
// paste mode); opening it on the laptop completes the provider's
// localhost redirect INTO the VM's waiting listener — one browser approval,
// no URL copy-paste. The token then lives on the session disk, so this is
// once per session, surviving park/resume. Same local/remote port: the
// redirect URL embeds the port claude registered on the VM.
func (d *Driver) MCPLoginCommand(ctx context.Context, id, server string, port int) (*exec.Cmd, error) {
	remote := fmt.Sprintf(
		"set -a; . ~/.config/pier/env 2>/dev/null; set +a; BROWSER=echo exec claude mcp login '%s' --callback-port %d",
		server, port)
	args := append(d.sshOpts(id),
		"-t", "-L", fmt.Sprintf("%d:localhost:%d", port, port), "agent@"+id, remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd, nil
}

// PortForwardCommand: plain ssh -L forwards, -N so no remote shell is
// taken. Runs until interrupted. The IAP tunnel is not line-rate; sshOpts'
// -C buys a few x on dev-server text.
func (d *Driver) PortForwardCommand(ctx context.Context, id string, pairs [][2]int) (*exec.Cmd, error) {
	args := d.sshOpts(id)
	for _, p := range pairs {
		args = append(args, "-L", fmt.Sprintf("%d:localhost:%d", p[0], p[1]))
	}
	args = append(args, "-N", "agent@"+id)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd, nil
}

func (d *Driver) SSHTarget(ctx context.Context, id string) ([]string, string, error) {
	return d.sshOpts(id), "agent@" + id, nil
}

func (d *Driver) Exec(ctx context.Context, id string, command string) (string, error) {
	return d.sshRun(ctx, id, command)
}

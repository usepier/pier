package pier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/tombstone"
)

// Sessions is the one list every surface shows: the cloud's sessions with
// their supervisor beacons read (working/idle, strain, setup state), plus a
// failed row per create that died before becoming a session. Unclaimed
// ready sessions are included — SplitReady separates them.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	sessions, err := c.drv.List(ctx)
	if err != nil {
		return nil, err
	}
	c.enrich(ctx, sessions)
	merged, revived := mergeTombstones(sessions, tombstone.List(config.Dir()))
	for _, name := range revived {
		tombstone.Dismiss(config.Dir(), name)
	}
	return merged, nil
}

// SplitReady separates real sessions from unclaimed ready sessions (warm
// pool members): those are inventory, not work, and never sit in the
// session table.
func SplitReady(all []Session) (sessions, ready []Session) {
	for _, s := range all {
		if s.PoolGen != "" {
			ready = append(ready, s)
		} else {
			sessions = append(sessions, s)
		}
	}
	return sessions, ready
}

// enrich upgrades StateRunning to working/idle by reading each running
// session's supervisor beacon in parallel.
func (c *Client) enrich(ctx context.Context, sessions []Session) {
	var wg sync.WaitGroup
	for i := range sessions {
		// Ready sessions run headless between create and park; their beacon
		// detail is nobody's business until they're claimed.
		if sessions[i].State != driver.StateRunning || sessions[i].PoolGen != "" {
			continue
		}
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			out, err := c.drv.Exec(ctx, s.ID, "cat /run/pier/status.json 2>/dev/null || echo absent")
			if err != nil {
				return // unreachable (ssm blip) — keep plain "running"
			}
			if strings.TrimSpace(out) == "absent" {
				// Only ready-tagged sessions get here, so no beacon isn't
				// mid-create anymore: /run is tmpfs, so right after a resume
				// the supervisor hasn't written its first beacon yet. Keep
				// plain "running".
				return
			}
			var st struct {
				State         string `json:"state"`
				Bootstrapping bool   `json:"bootstrapping"`
				Strained      bool   `json:"strained"`
				Setup         string `json:"setup"`
			}
			if json.Unmarshal([]byte(out), &st) != nil {
				return
			}
			// Supervisor up but bootstrap not done: the repo is still on its
			// way (or the create died) — either way, not attachable yet.
			if st.Bootstrapping {
				s.State = driver.StateCreating
				return
			}
			switch st.State {
			case "working":
				s.State = driver.StateWorking
			case "idle":
				s.State = driver.StateIdle
			}
			s.Strained = st.Strained
			s.Setup = st.Setup
		}(&sessions[i])
	}
	wg.Wait()
}

// mergeTombstones appends a failed row per tombstone and reports the ones the
// cloud has since answered for. A name that is live again means the create
// was retried and worked, so its gravestone is stale — the user shouldn't
// have to clear a row that the obvious next action already resolved.
func mergeTombstones(sessions []Session, recs []tombstone.Record) (merged []Session, revived []string) {
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.Name] = true
	}
	for _, r := range recs {
		if live[r.Name] {
			revived = append(revived, r.Name)
			continue
		}
		sessions = append(sessions, Session{
			Name: r.Name, Repo: r.Repo, Branch: r.Branch, Driver: r.Driver,
			State: driver.StateFailed, Created: r.When,
			FailReason: r.Reason, LogPath: r.LogPath,
			CostNote: "—", // nothing is running; nothing is being charged
		})
	}
	return sessions, revived
}

// Match resolves a user-typed session name: an exact name wins, otherwise a
// unique substring. Ready sessions are never matched by name.
func (c *Client) Match(ctx context.Context, query string) (Session, error) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return Session{}, err
	}
	return match(sessions, query)
}

func match(sessions []Session, query string) (Session, error) {
	var hits []Session
	for _, s := range sessions {
		if s.PoolGen != "" {
			continue
		}
		if s.Name == query {
			return s, nil
		}
		if strings.Contains(s.Name, query) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Session{}, fmt.Errorf("%w matching %q", ErrNotFound, query)
	default:
		var names []string
		for _, h := range hits {
			names = append(names, h.Name)
		}
		return Session{}, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, query, strings.Join(names, ", "))
	}
}

// CheckReady refuses a still-creating, failed, or deleting session cleanly,
// before any ssh is spawned, so the user never sees a raw transport error
// from the window between cloud-running and actually-attachable.
func CheckReady(s Session) error {
	switch s.State {
	case driver.StateFailed:
		hint := "`pier " + s.Name + "` retries it"
		if s.LogPath != "" {
			hint += ", " + s.LogPath + " has the create log"
		}
		return fmt.Errorf("%w: %s (%s) — nothing is running; %s",
			ErrCreateFailed, s.Name, tombstone.Summarize(s.FailReason), hint)
	case driver.StateCreating:
		return fmt.Errorf("%w: %s — try again when it shows running", ErrStillCreating, s.Name)
	case driver.StateDeleting:
		return fmt.Errorf("%w: %s", ErrDeleting, s.Name)
	}
	return nil
}

// Resume unparks a session and waits until its transport answers. A no-op
// for sessions that aren't parked.
func (c *Client) Resume(ctx context.Context, s Session) error {
	if s.State != driver.StateParked {
		return nil
	}
	return c.drv.Resume(ctx, s.ID)
}

// Remove destroys a session and its disk; for a failed create it forgets
// the record (there is no instance).
func (c *Client) Remove(ctx context.Context, s Session) error {
	if s.State == driver.StateFailed {
		return tombstone.Dismiss(config.Dir(), s.Name)
	}
	return c.drv.Destroy(ctx, s.ID)
}

// Keep disables idle self-park; the supervisor re-reads its conf every
// tick, so this applies live.
func (c *Client) Keep(ctx context.Context, s Session) error {
	if s.State == driver.StateParked {
		return fmt.Errorf("%w: %s — attach first", ErrParked, s.Name)
	}
	_, err := c.drv.Exec(ctx, s.ID,
		"sudo sed -i 's/^idle_timeout=.*/idle_timeout=never/' /etc/pier/supervisor.conf")
	return err
}

// Resize changes a session's machine type (park → modify → resume when
// running; a parked session stays parked).
func (c *Client) Resize(ctx context.Context, s Session, machineType string) error {
	if err := CheckReady(s); err != nil {
		return err // resizing mid-create would stop the instance under its bootstrap
	}
	return c.drv.Resize(ctx, s.ID, machineType)
}

// Machines is the resize catalog for a session: same-arch types with shape
// and rough cost.
func (c *Client) Machines(s Session) []Machine { return c.drv.Machines(s.InstanceType) }

// Headroom reports the account's vCPU quota use.
func (c *Client) Headroom(ctx context.Context) (Quota, error) { return c.drv.Headroom(ctx) }

// AttachCommand prepares one attach attempt (ssh + tmux) for the caller to
// run in the foreground.
func (c *Client) AttachCommand(ctx context.Context, s Session) (*exec.Cmd, error) {
	return c.drv.AttachCommand(ctx, s.ID)
}

// RetryAttach reports whether a finished attach attempt deserves one
// bounded wait-for-reachability and retry: a fresh or just-resumed VM
// reports cloud-running before its transport answers, so ssh exits 255
// within seconds. Other quick failures came from the remote command, and
// retrying would only hide them.
func RetryAttach(err error, elapsed time.Duration) bool {
	var exitErr *exec.ExitError
	return elapsed <= 15*time.Second && errors.As(err, &exitErr) && exitErr.ExitCode() == 255
}

// WaitReachable polls a no-op exec until the session's ssh transport
// answers.
func (c *Client) WaitReachable(ctx context.Context, s Session, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := c.drv.Exec(cctx, s.ID, "true")
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("session not reachable after %s — `pier ls` to check on it", timeout)
}

const noSetupLog = `echo "no setup log — this session ran no setup script"`

// SetupLog returns the tail of a session's setup log (at most lines lines;
// 0 = all). A failed create has no VM, so its local create log is served
// instead: "what broke" is the same question either way.
func (c *Client) SetupLog(ctx context.Context, s Session, lines int) (string, error) {
	if s.State == driver.StateFailed {
		if s.LogPath != "" {
			if b, err := os.ReadFile(s.LogPath); err == nil {
				return string(b), nil
			}
		}
		return s.FailReason, nil
	}
	if err := CheckReady(s); err != nil {
		return "", err
	}
	read := "cat ~/.pier-setup.log"
	if lines > 0 {
		read = fmt.Sprintf("tail -n %d ~/.pier-setup.log", lines)
	}
	out, err := c.drv.Exec(ctx, s.ID, `[ -f ~/.pier-setup.log ] && `+read+` || `+noSetupLog)
	if err == nil && lines > 0 && strings.Count(out, "\n") >= lines-1 {
		out = "… older lines trimmed — pier logs " + s.Name + " prints everything\n" + out
	}
	return out, err
}

// FollowLogCommand prepares a raw `tail -f` of the setup log — pipeable,
// with the terminal interpreting progress-meter escapes natively.
func (c *Client) FollowLogCommand(ctx context.Context, s Session) (*exec.Cmd, error) {
	opts, dest, err := c.drv.SSHTarget(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	remote := `[ -f ~/.pier-setup.log ] && exec tail -n +1 -f ~/.pier-setup.log || ` + noSetupLog
	return exec.Command("ssh", append(opts, dest, remote)...), nil
}

var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9._:@-]+$`)

// PendingMCP asks the session which OAuth-backed MCP servers still lack a
// token (seeded ~/.claude.json minus ~/.claude/.credentials.json).
func (c *Client) PendingMCP(ctx context.Context, s Session) ([]string, error) {
	out, err := c.drv.Exec(ctx, s.ID,
		"cat ~/.claude.json 2>/dev/null; printf '\\n---PIER-SPLIT---\\n'; cat ~/.claude/.credentials.json 2>/dev/null")
	if err != nil {
		return nil, err
	}
	cfgRaw, credRaw, _ := strings.Cut(out, "---PIER-SPLIT---")
	var pending []string
	for _, n := range payload.OAuthRemoteNames([]byte(cfgRaw), payload.Workspace+"/"+s.Repo) {
		if !mcpAuthed(credRaw, n) {
			pending = append(pending, n)
		}
	}
	return pending, nil
}

// OAuthMCPServers names the laptop's OAuth-backed MCP servers that a new
// session of repoRoot would need one browser approval for.
func OAuthMCPServers(repoRoot string) []string {
	home, _ := os.UserHomeDir()
	return payload.OAuthRemotes(home, repoRoot)
}

// MCPLoginCommand prepares the browser OAuth flow for one MCP server, with
// the callback port tunneled into the session.
func (c *Client) MCPLoginCommand(ctx context.Context, s Session, server string) (*exec.Cmd, error) {
	if !mcpServerName.MatchString(server) {
		return nil, fmt.Errorf("implausible server name %q", server)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	return c.drv.MCPLoginCommand(ctx, s.ID, server, port)
}

// mcpAuthed checks the session's credential store for a token under this
// server. Claude keys mcpOAuth by server name (possibly suffixed); an
// unrecognized format fails open — worst case one redundant approval.
func mcpAuthed(credJSON, server string) bool {
	var c struct {
		McpOAuth map[string]json.RawMessage `json:"mcpOAuth"`
	}
	if json.Unmarshal([]byte(credJSON), &c) != nil {
		return false
	}
	for k := range c.McpOAuth {
		if k == server || strings.HasPrefix(k, server+"|") || strings.HasPrefix(k, server+":") {
			return true
		}
	}
	return false
}

// PortForwardCommand prepares ssh -L forwards held open until interrupted.
// pairs are {local, remote}.
func (c *Client) PortForwardCommand(ctx context.Context, s Session, pairs [][2]int) (*exec.Cmd, error) {
	return c.drv.PortForwardCommand(ctx, s.ID, pairs)
}

// ParsePortPair reads "3000" or "local:session" like "8080:3000".
func ParsePortPair(s string) ([2]int, error) {
	local, remote, split := strings.Cut(s, ":")
	if !split {
		remote = local
	}
	var p [2]int
	for i, v := range []string{local, remote} {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return p, fmt.Errorf("bad port %q — want 3000 or local:session like 8080:3000", s)
		}
		p[i] = n
	}
	return p, nil
}

// freePort grabs an OS-assigned port. Local and remote must match: the OAuth
// redirect URL embeds the one port claude registers inside the VM.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

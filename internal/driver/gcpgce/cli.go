package gcpgce

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	execCommand        = exec.Command
	execCommandContext = exec.CommandContext
)

// gcloud runs the gcloud CLI (the tool already requires it for the IAP
// tunnel, so v1 has no SDK dependency) and returns trimmed stdout. --quiet
// suppresses every confirmation prompt; the project is always explicit so
// pier never depends on (or disturbs) the operator's active gcloud config.
func (d *Driver) gcloud(ctx context.Context, args ...string) (string, error) {
	full := append([]string{}, args...)
	full = append(full, "--quiet")
	if d.Project != "" {
		full = append(full, "--project", d.Project)
	}
	cmd := execCommandContext(ctx, "gcloud", full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		n := min(len(args), 3)
		return "", fmt.Errorf("gcloud %s: %s", strings.Join(args[:n], " "), gcloudErr(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// gcloudErr compacts gcloud's stderr to its ERROR line. Crashes append
// multi-paragraph "run gcloud feedback" boilerplate that would otherwise
// land verbatim in the TUI status line and the CLI's one-line errors.
//
// Expired credentials get rewritten to their remedy. Workspace accounts
// carry an org session policy (Google's newer default is 16 hours), so an
// expired gcloud session is a routine morning state — and the raw error
// ("Reauthentication failed. cannot prompt during non-interactive
// execution") describes pier's subprocess plumbing, not the fix.
func gcloudErr(stderr string) string {
	for _, marker := range []string{
		"Reauthentication",
		"problem refreshing your current auth tokens",
	} {
		if strings.Contains(stderr, marker) {
			return "gcloud auth has expired — run `gcloud auth login`, then retry"
		}
	}
	first := ""
	for _, line := range strings.Split(stderr, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "ERROR:") {
			return l
		}
		if first == "" {
			first = l
		}
	}
	return first
}

// user returns the caller identity used for label namespacing: the active
// gcloud account (what `gcloud auth login` established).
func (d *Driver) user(ctx context.Context) (string, error) {
	if d.principal != "" {
		return d.principal, nil
	}
	out, err := d.gcloud(ctx, "auth", "list", "--filter=status:ACTIVE", "--format=value(account)")
	if err != nil {
		return "", err
	}
	acct, _, _ := strings.Cut(out, "\n")
	if acct == "" {
		return "", fmt.Errorf("no active gcloud account — run `gcloud auth login`")
	}
	d.principal = acct
	return acct, nil
}

// --- SSH into the session ----------------------------------------------------
// One mechanism for everything interactive and file-shaped: OpenSSH. No
// standing keys — each session gets its own keypair at create, stored under
// StateDir/keys/<instance-name>.pem, its pubkey delivered via instance
// metadata (the guest agent maintains authorized_keys from it). Transport is
// always the IAP tunnel: raw ssh with a start-iap-tunnel ProxyCommand.
// NEVER `gcloud compute ssh` — it insists on the operator's personal
// ~/.ssh/google_compute_engine key, and a passphrase on that key breaks
// every non-interactive use.

func (d *Driver) keyPath(id string) string {
	return filepath.Join(d.StateDir, "keys", id+".pem")
}

func (d *Driver) sshOpts(id string) []string {
	pc := fmt.Sprintf(
		"gcloud compute start-iap-tunnel %%h %%p --listen-on-stdin --project=%s --zone=%s --verbosity=error",
		d.Project, d.Zone)
	return []string{
		"-i", d.keyPath(id),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(d.StateDir, "known_hosts"),
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		// Tunnel setup alone takes a few seconds; 5s would false-negative
		// every probe (verified by spike/gcp.sh).
		"-o", "ConnectTimeout=10",
		// A dead connection otherwise hangs transfers forever: probe every
		// 15s, give up after 4 misses (~60s).
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "ProxyCommand=" + pc,
		// The IAP tunnel is not line-rate; compression buys a few x on the
		// text a dev workflow pushes through it (JS modules, tmux screens).
		"-C",
	}
}

func (d *Driver) sshRun(ctx context.Context, id, script string) (string, error) {
	return d.sshRunOpts(ctx, id, nil, script)
}

// sshRunOpts is sshRun with extra ssh flags — the bootstrap passes -A when
// the workspace fetch rides the laptop's ssh agent.
func (d *Driver) sshRunOpts(ctx context.Context, id string, extra []string, script string) (string, error) {
	args := append(append(d.sshOpts(id), extra...), "agent@"+id, script)
	out, err := execCommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh %s: %s", id, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// sshStream is sshRun with output flowing straight to the terminal — for
// long user-visible steps (the bake hook) where buffered output would look
// like a hang.
func (d *Driver) sshStream(ctx context.Context, id, script string) error {
	args := append(d.sshOpts(id), "agent@"+id, script)
	cmd := execCommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// extra flags pass through to scp — "-q" silences the progress meter for
// pushes too small to warrant one.
func (d *Driver) scpTo(ctx context.Context, id, local, remote string, extra ...string) error {
	args := append(append(d.sshOpts(id), extra...), local, "agent@"+id+":"+remote)
	cmd := execCommandContext(ctx, "scp", args...)
	// scp draws its progress meter only when stdout is a terminal — so big
	// pushes (the repo bundle) show live progress interactively and stay
	// silent when piped.
	cmd.Stdout = os.Stdout
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp %s -> %s: %s", local, id, strings.TrimSpace(errb.String()))
	}
	return nil
}

// newKeypair generates the per-session ed25519 keypair via ssh-keygen and
// returns the public key line.
func (d *Driver) newKeypair(id string) (string, error) {
	dir := filepath.Join(d.StateDir, "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	key := d.keyPath(id)
	_ = os.Remove(key)
	_ = os.Remove(key + ".pub")
	out, err := execCommand("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "pier", "-f", key).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh-keygen: %s", strings.TrimSpace(string(out)))
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}

// waitSSH polls until SSH over the IAP tunnel answers.
func (d *Driver) waitSSH(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := d.sshRun(c, id, "true")
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("session %s not reachable after %s", id, timeout)
}

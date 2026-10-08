package pier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/proxy"
)

// Running sessions as <name>.pier on this machine, from a frontend that
// keeps its own connections (Options.Mux): the proxy rides them instead of
// opening a second ssh connection per session.

type (
	ProxyStatus  = proxy.Status
	ProxySession = proxy.SessionStatus
)

// ErrAdminCancelled: the administrator prompt was dismissed.
var ErrAdminCancelled = errors.New("cancelled at the administrator prompt")

// RunProxy serves every running session as <session>.pier, its listening
// ports mirrored, until ctx ends. privileged runs the one-time network
// setup with admin rights; onChange gets the proxy's state as it changes.
func (c *Client) RunProxy(ctx context.Context, privileged func(script string) error, onChange func(ProxyStatus)) error {
	if c.mux == nil {
		return errors.New("this client keeps no connections (Options.Mux is off)")
	}
	return proxy.Run(ctx, c.drv, proxy.Options{
		StateDir: config.Dir(), Connect: c.proxyLink, Privileged: privileged, OnChange: onChange,
	})
}

// muxLink is a session's connection, as the proxy uses it. Each use asks
// the manager again, so a connection made anew after a drop is picked up.
type muxLink struct {
	c  *Client
	id string
}

func (c *Client) proxyLink(ctx context.Context, id string) (proxy.Link, error) {
	if _, err := c.mux.Conn(ctx, id); err != nil {
		return nil, err
	}
	return &muxLink{c, id}, nil
}

func (l *muxLink) Exec(ctx context.Context, script string) ([]byte, error) {
	conn, err := l.c.mux.Conn(ctx, l.id)
	if err != nil {
		return nil, err
	}
	conn.Busy()
	return conn.Exec(ctx, script, nil)
}

func (l *muxLink) Forward(spec string) error {
	conn := l.c.mux.Existing(l.id)
	if conn == nil {
		return errors.New("the session's connection is gone")
	}
	return conn.Forward("-L", spec)
}

func (l *muxLink) Cancel(spec string) error {
	if conn := l.c.mux.Existing(l.id); conn != nil {
		return conn.Cancel("-L", spec)
	}
	return nil
}

func (l *muxLink) Generation() int {
	if conn := l.c.mux.Existing(l.id); conn != nil {
		return conn.Generation()
	}
	return -1
}

// ForwardPort forwards 127.0.0.1:local to the session's localhost:remote
// over its connection — `pier port`, for a frontend. It lasts as long as
// the connection.
func (c *Client) ForwardPort(ctx context.Context, s Session, local, remote int) error {
	if local < 1 || local > 65535 || remote < 1 || remote > 65535 {
		return fmt.Errorf("ports are 1-65535")
	}
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return err
	}
	return conn.Forward("-L", fmt.Sprintf("127.0.0.1:%d:localhost:%d", local, remote))
}

// UnforwardPort removes a ForwardPort forward.
func (c *Client) UnforwardPort(s Session, local, remote int) error {
	if c.mux == nil {
		return nil
	}
	conn := c.mux.Existing(s.ID)
	if conn == nil {
		return nil
	}
	return conn.Cancel("-L", fmt.Sprintf("127.0.0.1:%d:localhost:%d", local, remote))
}

// AdminShell runs a shell script as root after the system's administrator
// prompt (macOS), saying why — for a frontend with no terminal to sudo in.
func AdminShell(script, why string) error {
	as := "do shell script " + appleString("set -e; "+strings.ReplaceAll(strings.TrimSpace(script), "\n", "; ")) +
		" with administrator privileges with prompt " + appleString(why)
	out, err := exec.Command("osascript", "-e", as).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") || strings.Contains(strings.ToLower(msg), "user canceled") {
			return ErrAdminCancelled
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// TrustProxyCA makes this Mac trust the proxy's certificate authority, so
// https://<session>.pier opens without a warning. macOS asks for the
// user's password itself.
func TrustProxyCA(caPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	out, err := exec.Command("security", "add-trusted-cert", "-r", "trustRoot",
		"-k", filepath.Join(home, "Library/Keychains/login.keychain-db"), caPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("trusting the certificate: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CLIStatus is the pier command on this Mac's PATH, next to the pier a
// frontend bundles.
type CLIStatus struct {
	// Link is where the command goes (/usr/local/bin/pier).
	Link string
	// Installed: Link points at the bundled pier.
	Installed bool
	// Other is another pier the shell finds first, if any (a Homebrew one).
	Other string
}

// CLILink is where InstallCLI puts the pier command.
const CLILink = "/usr/local/bin/pier"

// CLIInstallStatus says whether self (the bundled pier) is the pier
// command.
func CLIInstallStatus(self string) CLIStatus {
	st := CLIStatus{Link: CLILink}
	if target, err := filepath.EvalSymlinks(CLILink); err == nil {
		if want, err := filepath.EvalSymlinks(self); err == nil && target == want {
			st.Installed = true
		}
	}
	if found, err := exec.LookPath("pier"); err == nil && found != CLILink {
		if real, _ := filepath.EvalSymlinks(found); real != "" {
			if want, _ := filepath.EvalSymlinks(self); real != want {
				st.Other = found
			}
		}
	}
	return st
}

// InstallCLI links CLILink to self, the bundled pier, asking for admin
// rights when /usr/local/bin isn't writable.
func InstallCLI(self string) error {
	if !filepath.IsAbs(self) {
		return fmt.Errorf("%q isn't an absolute path", self)
	}
	dir := filepath.Dir(CLILink)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		tmp := CLILink + ".pier-new"
		_ = os.Remove(tmp)
		if os.Symlink(self, tmp) == nil && os.Rename(tmp, CLILink) == nil {
			return nil
		}
		_ = os.Remove(tmp)
	}
	return AdminShell("mkdir -p "+shellQuote(dir)+" && ln -sfn "+shellQuote(self)+" "+shellQuote(CLILink),
		"pier wants to install the pier command in "+dir+".")
}

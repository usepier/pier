package pier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrAdminCancelled: the administrator prompt was dismissed.
var ErrAdminCancelled = errors.New("cancelled at the administrator prompt")

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
	// Provided is a pier on PATH that is the bundled one by another way
	// (the link a Homebrew cask makes): nothing to install.
	Provided string
	// Stale: Link points at an app's pier that moved or is gone (the app
	// was moved or replaced); installing again repairs it.
	Stale bool
	// Other is another pier the shell finds first, if any (a Homebrew
	// formula's).
	Other string
}

// CLILink is where InstallCLI puts the pier command; a var so tests put
// it elsewhere.
var CLILink = "/usr/local/bin/pier"

// ErrNeedsAdmin: putting the command in place needs an administrator, and
// asking wasn't allowed.
var ErrNeedsAdmin = errors.New("installing the pier command needs an administrator")

// CLIInstallStatus says whether self (the bundled pier) is the pier
// command.
func CLIInstallStatus(self string) CLIStatus {
	st := CLIStatus{Link: CLILink}
	want, _ := filepath.EvalSymlinks(self)
	if fi, err := os.Lstat(CLILink); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(CLILink)
		real, err := filepath.EvalSymlinks(CLILink)
		switch {
		case err == nil && real == want:
			st.Installed = true
		case strings.Contains(target, ".app/Contents/MacOS/"):
			// An app's pier, but not this one, or not there any more.
			st.Stale = err != nil || real != want
		}
	}
	if found, err := exec.LookPath("pier"); err == nil && found != CLILink {
		if real, _ := filepath.EvalSymlinks(found); real != "" && real == want {
			st.Provided = found
		} else if real != "" {
			st.Other = found
		}
	}
	return st
}

// InstallCLI links CLILink to self, the bundled pier. When
// /usr/local/bin isn't writable it asks for admin rights — or, when it
// mayn't ask, fails with ErrNeedsAdmin.
func InstallCLI(self string, mayAsk bool) error {
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
	if !mayAsk {
		return ErrNeedsAdmin
	}
	return AdminShell("mkdir -p "+shellQuote(dir)+" && ln -sfn "+shellQuote(self)+" "+shellQuote(CLILink),
		"pier wants to install the pier command in "+dir+".")
}

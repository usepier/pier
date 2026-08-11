// Package payload assembles everything a new session VM receives: the
// repo-transfer decision (github fetch, thin bundle, or full bundle), the
// dirty-tracked patch, the files tar (secrets manifest + .pier-include extras
// + session env), the cloud-init user-data, and the bootstrap script that
// puts it all in place. Cloud-agnostic by construction — drivers launch,
// push, and run; nothing here knows which cloud is on the other end.
package payload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/usepier/pier/internal/driver"
)

// Workspace is where sessions check the repo out. Shared by every driver so
// laptop-side seeds (claude project trust) can name the path pre-launch.
const Workspace = "/home/agent/work"

// Push is one local file bound for the session.
type Push struct {
	Local, Remote string
}

// Payload is the assembled create-time cargo. Drivers print Notes, push
// Pushes in order, then run /tmp/pier-bootstrap.sh as agent (with the ssh
// agent forwarded when ForwardAgent says the fetch borrows it).
type Payload struct {
	ForwardAgent bool     // bootstrap fetch rides the laptop's ssh agent (-A)
	Notes        []string // progress lines to print right before pushing
	Pushes       []Push   // push in order — biggest last, its meter is the wait
}

// Build resolves how the repo travels and writes every artifact into dir (a
// caller-owned temp dir), while the caller's instance boots. The tunnel a
// driver pushes through can be slow (SSM moves ~1 MB/s), so ship as little as
// possible through it: when the base commit is already on a GitHub origin the
// VM fetches the repo from GitHub directly (~100x faster) and the laptop
// sends at most a thin local-delta bundle.
func Build(ctx context.Context, dir string, spec driver.CreateSpec, supervisor []byte, manifest []string, env map[string]string, progress func(string)) (*Payload, error) {
	sha, err := gitOut(spec.Repo, "rev-parse", "--verify", baseRef(spec)+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("base ref %q not found", baseRef(spec))
	}
	mode, origin := originInfo(spec.Repo, sha)
	forwardAgent := false
	why := "no github origin has the base"
	if mode != "full" {
		switch {
		case originReachable(ctx, origin, env["GH_TOKEN"]):
			// https works with the auth the session gets (token or anonymous)
		case sshOriginUsable(ctx, spec.Repo):
			// No https credential, but the laptop's ssh agent can auth: keep
			// the ssh origin and forward the agent for the bootstrap fetch.
			origin, _ = gitOut(spec.Repo, "remote", "get-url", "origin")
			forwardAgent = true
			progress("no github token — the fetch borrows your ssh agent (keys stay on this machine)")
		default:
			progress("github origin not reachable with the auth sessions get — using the full bundle")
			mode, why = "full", "github not reachable with session auth"
		}
	}
	bundle := ""
	if mode != "origin" {
		bundle = filepath.Join(dir, "pier.bundle")
		switch err := gitBundle(spec.Repo, sha, bundle, exportRef(spec.Name), mode == "thin"); {
		// Only thin bundles are legitimately empty (stale --contains; origin
		// has it all). A full bundle of real history can't be — treating that
		// as "fetch from origin" once shipped a session pointed at an origin
		// that didn't exist.
		case errors.Is(err, errEmptyBundle) && mode == "thin":
			mode, bundle = "origin", ""
		case err != nil:
			return nil, fmt.Errorf("bundling %s: %w", spec.Repo, err)
		}
	}
	// Dirty tracked state travels only when the session's base IS the
	// laptop's HEAD — branching a session off another commit and grafting
	// today's edits onto it would be a lie about what that base contained.
	patch := ""
	if head, _ := gitOut(spec.Repo, "rev-parse", "HEAD"); head == sha {
		p := filepath.Join(dir, "pier-dirty.patch")
		switch ok, err := dirtyPatch(spec.Repo, p); {
		case err != nil:
			return nil, err
		case ok:
			patch = p
		}
	}
	setupSrc, warn := setupScriptOverride(spec.Repo)
	if warn != "" {
		progress(warn)
	}
	filesTar := filepath.Join(dir, "pier-files.tar")
	if err := buildFilesTar(filesTar, manifest, spec.Repo, env, setupSrc); err != nil {
		return nil, err
	}
	if miss := envFilesNotCarried(spec.Repo, pierIncludeFiles(spec.Repo)); len(miss) > 0 {
		name := miss[0]
		if len(miss) > 1 {
			name += fmt.Sprintf(" +%d more", len(miss)-1)
		}
		progress("not carrying " + name + " — env files travel only when .pier-include lists them")
	}
	supPath := filepath.Join(dir, "pier-supervisor")
	if err := os.WriteFile(supPath, supervisor, 0o755); err != nil {
		return nil, err
	}
	bootPath := filepath.Join(dir, "pier-bootstrap.sh")
	if err := os.WriteFile(bootPath, []byte(renderBootstrap(spec, mode, sha, origin)), 0o755); err != nil {
		return nil, err
	}

	p := &Payload{ForwardAgent: forwardAgent}
	p.Pushes = []Push{
		{supPath, "/tmp/pier-supervisor"},
		{bootPath, "/tmp/pier-bootstrap.sh"},
		{filesTar, "/tmp/pier-files.tar"},
	}
	switch mode {
	case "origin":
		p.Notes = append(p.Notes, "pushing secrets — the repo comes straight from github on the VM")
	case "thin":
		p.Notes = append(p.Notes, fmt.Sprintf("pushing local-only commits (%s) — the rest comes from github", fileMB(bundle)))
		p.Pushes = append(p.Pushes, Push{bundle, "/tmp/pier.bundle"})
	default:
		p.Notes = append(p.Notes, fmt.Sprintf("pushing workspace (%s — full history; %s)", fileMB(bundle), why))
		p.Pushes = append(p.Pushes, Push{bundle, "/tmp/pier.bundle"})
	}
	if patch != "" {
		p.Notes = append(p.Notes, "carrying your uncommitted edits to tracked files")
		p.Pushes = append(p.Pushes, Push{patch, "/tmp/pier-dirty.patch"})
	}
	home, _ := os.UserHomeDir()
	if names := OAuthRemotes(home, spec.Repo); len(names) > 0 {
		p.Notes = append(p.Notes, "mcp "+strings.Join(names, ", ")+": one-time oauth — `pier mcp login "+spec.Name+"` when it's up")
	}
	return p, nil
}

func baseRef(spec driver.CreateSpec) string {
	if spec.BaseRef == "" {
		return "HEAD"
	}
	return spec.BaseRef
}

// ValidateNames gates what create splices into the VM bootstrap (a bash
// script) and into tag values, hostnames, and key file names. A conservative
// charset beats quoting three shell layers deep: a legal-but-hostile git
// branch like "a'; rm -rf ~" must never reach the template, and it fails
// here, before anything launches and bills.
func ValidateNames(spec driver.CreateSpec) error {
	if err := checkName(spec.Branch); err != nil {
		return fmt.Errorf("branch %q: %w", spec.Branch, err)
	}
	if spec.Name != spec.Branch {
		if err := checkName(spec.Name); err != nil {
			return fmt.Errorf("session name %q: %w", spec.Name, err)
		}
	}
	base := filepath.Base(spec.Repo)
	for _, r := range base {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("'\"$\\`", r) {
			return fmt.Errorf("repo directory %q contains %q, which breaks the VM setup script it is spliced into — rename the directory first", base, r)
		}
	}
	return nil
}

// checkName allows the git-branch shapes people actually use. Everything else
// is rejected: the name also becomes a proxy hostname, a provider tag, a tmux
// target, and a log file name, so exotic characters fail somewhere far worse
// than here.
func checkName(s string) error {
	if s == "" || len(s) > 100 {
		return fmt.Errorf("must be 1-100 characters")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/':
		default:
			return fmt.Errorf("only letters, digits and . _ - / are allowed")
		}
	}
	if s[0] == '-' || s[0] == '.' || s[0] == '/' {
		return fmt.Errorf("cannot start with %q", s[0])
	}
	if strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") {
		return fmt.Errorf("cannot end with / . or .lock")
	}
	if strings.Contains(s, "..") || strings.Contains(s, "//") {
		return fmt.Errorf("cannot contain .. or //")
	}
	return nil
}

var unsafeHost = regexp.MustCompile(`[^a-z0-9-]+`)

// Sanitize turns a session name (may contain "/") into a hostname, an
// instance-name segment, or a filename.
func Sanitize(name string) string {
	s := unsafeHost.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "session"
	}
	return s
}

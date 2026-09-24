package payload

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// claudeSeed builds a minimal ~/.claude.json for the VM: theme +
// onboarding-done (skips the theme picker), the MCP servers that can actually
// run on Linux — user-scope ones plus the source repo's project-scoped ones,
// which follow the repo onto the VM workdir (their auth lives in env blocks /
// headers and travels; OAuth'd remotes re-auth in-session) — and pre-trust
// for the workdir: the trust dialog would only re-ask what creating the
// session already answered. The rest of the local state file (history,
// per-path project state) is laptop-specific noise and deliberately stays home.
func claudeSeed(home, srcRepo, workdir string) []byte {
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil
	}
	var local struct {
		Theme                  string                     `json:"theme"`
		HasCompletedOnboarding bool                       `json:"hasCompletedOnboarding"`
		McpServers             map[string]json.RawMessage `json:"mcpServers"`
		Projects               map[string]struct {
			McpServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(b, &local) != nil || !local.HasCompletedOnboarding {
		return nil
	}
	proj := map[string]any{
		"hasTrustDialogAccepted":        true,
		"hasCompletedProjectOnboarding": true,
	}
	if mcp := portableMCP(local.Projects[srcRepo].McpServers); len(mcp) > 0 {
		proj["mcpServers"] = mcp
	}
	seed := map[string]any{
		"hasCompletedOnboarding": true,
		"projects":               map[string]any{workdir: proj},
	}
	if local.Theme != "" {
		seed["theme"] = local.Theme
	}
	if mcp := portableMCP(local.McpServers); len(mcp) > 0 {
		seed["mcpServers"] = mcp
	}
	out, _ := json.Marshal(seed)
	return out
}

// OAuthRemotes names the seeded remote MCP servers with no static auth
// header. Their OAuth tokens live in the OS keychain and rotate on refresh,
// so copying them would let two machines revoke each other — each session
// instead needs one `pier mcp login <name>` round. Static-auth servers (env
// blocks, Authorization headers) travel whole and never appear here.
func OAuthRemotes(home, srcRepo string) []string {
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil
	}
	return OAuthRemoteNames(b, srcRepo)
}

// OAuthRemoteNames is the pure parser behind OAuthRemotes: it reads any
// ~/.claude.json bytes (laptop or session — pass the matching project dir),
// so the CLI can ask a running session what still needs a browser approval.
func OAuthRemoteNames(b []byte, projectDir string) []string {
	var local struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			McpServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(b, &local) != nil {
		return nil
	}
	set := map[string]bool{}
	for _, servers := range []map[string]json.RawMessage{local.McpServers, local.Projects[projectDir].McpServers} {
		for name, raw := range servers {
			var s struct {
				Type    string            `json:"type"`
				Headers map[string]string `json:"headers"`
			}
			if json.Unmarshal(raw, &s) == nil && (s.Type == "http" || s.Type == "sse") && len(s.Headers) == 0 {
				set[name] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// portableMCP drops stdio servers whose command is a macOS-only path — on
// the Linux VM they would just render as failed servers.
func portableMCP(servers map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, raw := range servers {
		var s struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(raw, &s)
		if c := s.Command; strings.HasPrefix(c, "/Applications/") || strings.HasPrefix(c, "/Users/") ||
			strings.HasPrefix(c, "/opt/homebrew/") || strings.HasPrefix(c, "/System/") {
			continue
		}
		out[name] = raw
	}
	return out
}

// pierIncludeFiles lists the repo files (repo-relative) named by .pier/include.
// It is the opt-in channel for ignored files; non-ignored untracked files
// travel automatically. One path or glob per line (relative to the root;
// * ? [] per segment, no **), # comments, a directory line carries its whole
// subtree. A file symlink directly matched by a line or glob is dereferenced
// and carried as a regular file at the symlink's repo-relative path; directory
// symlinks and nested symlinks discovered while walking a directory are not
// followed. A listed path travels with no git-status distinction, and the tar
// extracts after checkout + patch, so listed content wins. No file, or an
// empty one, means no explicit extras travel.
func pierIncludeFiles(repoRoot string) []string {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".pier", "include"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), "/")
		if line == "" || strings.HasPrefix(line, "#") || !filepath.IsLocal(line) {
			continue
		}
		// WalkDir on a glob match handles files and directories uniformly
		// (a file path walks as just itself). Only a symlink that is itself a
		// match is eligible: directory walks must not escape through links in
		// their subtrees.
		matches, _ := filepath.Glob(filepath.Join(repoRoot, line))
		for _, m := range matches {
			match := filepath.Clean(m)
			_ = filepath.WalkDir(m, func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if e.IsDir() {
					if e.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if e.Type()&fs.ModeSymlink != 0 {
					target, statErr := os.Stat(path)
					if filepath.Clean(path) != match || statErr != nil || !target.Mode().IsRegular() {
						return nil
					}
				} else if !e.Type().IsRegular() {
					return nil
				}
				if rel, err := filepath.Rel(repoRoot, path); err == nil && filepath.IsLocal(rel) {
					seen[rel] = true
				}
				return nil
			})
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// untrackedFiles returns every non-ignored file Git sees as untracked. These
// are local work just like edits to tracked files, so sessions created from
// HEAD carry them automatically. --exclude-standard honors repository,
// .git/info/exclude, and global ignore rules; ignored files remain available
// only through the explicit .pier/include channel.
func untrackedFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--others", "--exclude-standard", "--").Output()
	if err != nil {
		return nil, fmt.Errorf("listing untracked files: %w", err)
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || strings.HasSuffix(rel, "/") || !filepath.IsLocal(rel) {
			continue
		}
		files = append(files, rel)
	}
	sort.Strings(files)
	return files, nil
}

func mergeRepoFiles(groups ...[]string) []string {
	seen := map[string]bool{}
	for _, group := range groups {
		for _, rel := range group {
			seen[rel] = true
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// envFilesNotCarried is the fail-loud affordance for ignored env files the
// tar is NOT carrying. Non-ignored untracked files travel automatically; a
// session whose app dies on an ignored, unlisted env file should have said so
// at create. Wholly-ignored dirs collapse (--directory) so node_modules
// fixtures don't count; tracked ones arrive with the fetch.
func envFilesNotCarried(repoRoot string, carried []string) []string {
	have := map[string]bool{}
	for _, p := range carried {
		have[p] = true
	}
	listing, err := gitOut(repoRoot, "ls-files", "-z", "-o", "-i", "--exclude-standard", "--directory")
	if err != nil {
		return nil
	}
	var miss []string
	for _, p := range strings.Split(listing, "\x00") {
		if p == "" || strings.HasSuffix(p, "/") {
			continue
		}
		if ok, _ := filepath.Match(".env*", filepath.Base(p)); ok && !have[p] {
			miss = append(miss, p)
		}
	}
	sort.Strings(miss)
	return miss
}

// setupScriptOverride resolves PIER_SETUP_SCRIPT. Relative overrides resolve
// against the repo root, ~ against home. A set-but-missing override warns and
// falls back to the repo's .pier/setup.sh — a typo shouldn't brick creates,
// but it must not be silent.
func setupScriptOverride(repoRoot string) (path, warn string) {
	v := os.Getenv("PIER_SETUP_SCRIPT")
	if v == "" {
		return "", ""
	}
	if strings.HasPrefix(v, "~/") {
		home, _ := os.UserHomeDir()
		v = filepath.Join(home, v[2:])
	} else if !filepath.IsAbs(v) {
		v = filepath.Join(repoRoot, v)
	}
	if fi, err := os.Stat(v); err != nil || !fi.Mode().IsRegular() {
		return "", "PIER_SETUP_SCRIPT: " + v + " not found — running the repo's .pier/setup.sh (if any) instead"
	}
	return v, ""
}

// buildFilesTar packs, into one tar: manifest files/dirs under $HOME (prefix
// home/), selected repo files (non-ignored untracked files plus .pier/include
// extras, relative paths kept under repo/), a PIER_SETUP_SCRIPT override (as
// home/.config/pier/setup.sh), and a generated home/.config/pier/env with the
// session tokens. The bootstrap extracts the two prefixes to the right places.
func buildFilesTar(dst string, manifest []string, repoRoot string, repoFiles []string, env map[string]string, setupSrc string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	home, _ := os.UserHomeDir()
	addFile := func(path, name string, mode fs.FileMode) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(b))}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	}

	for _, m := range manifest {
		p := m
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		} else if !filepath.IsAbs(p) {
			p = filepath.Join(home, p)
		}
		rel, err := filepath.Rel(home, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("manifest entry %q is outside $HOME", m)
		}
		err = filepath.WalkDir(p, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil // missing manifest entries are fine
			}
			if e.IsDir() {
				if e.Name() == ".git" { // plugin/marketplace checkouts
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() {
				return nil // symlinks, sockets
			}
			r, _ := filepath.Rel(home, path)
			info, _ := e.Info()
			return addFile(path, "home/"+r, info.Mode())
		})
		if err != nil {
			return err
		}
	}

	if seed := claudeSeed(home, repoRoot, Workspace+"/"+filepath.Base(repoRoot)); seed != nil {
		if err := tw.WriteHeader(&tar.Header{Name: "home/.claude.json", Mode: 0o600, Size: int64(len(seed))}); err != nil {
			return err
		}
		if _, err := tw.Write(seed); err != nil {
			return err
		}
	}

	for _, rel := range repoFiles {
		p := filepath.Join(repoRoot, rel)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			// Widen owner-only modes: the VM's docker daemon is
			// userns-remapped, so container uids are unprivileged host uids
			// and a laptop-tight 0600 .env bind-mounts unreadable — Docker
			// Desktop ignores host perms, so the repo works locally and
			// fails only here. The VM is single-user; o+r gives nothing away.
			mode := fi.Mode().Perm() | 0o444
			if mode&0o100 != 0 {
				mode |= 0o111
			}
			if err := addFile(p, "repo/"+rel, mode); err != nil {
				return err
			}
		}
	}

	if setupSrc != "" {
		// Normalize the override mode even though the bootstrap deliberately
		// invokes it with bash and only gates on presence.
		if err := addFile(setupSrc, "home/.config/pier/setup.sh", 0o755); err != nil {
			return err
		}
	}

	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k + "='" + strings.ReplaceAll(env[k], "'", `'\''`) + "'\n")
		}
		if err := tw.WriteHeader(&tar.Header{Name: "home/.config/pier/env", Mode: 0o600, Size: int64(b.Len())}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(b.String())); err != nil {
			return err
		}
	}
	return nil
}

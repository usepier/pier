package wizard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/skills"
)

// AgentDirs reports which agent config dirs exist under home (".claude",
// ".codex") — the agents this machine actually runs. pier never conjures
// one that isn't there.
func AgentDirs(home string) []string {
	var found []string
	for _, agent := range []string{".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(home, agent)); err == nil {
			found = append(found, agent)
		}
	}
	return found
}

// InstallSkills lays the bundled skills (skills.FS — notably pier-onboard,
// how an agent learns to write a repo's .pier files) into each named agent's
// user skills directory. Claude Code and Codex read the same SKILL.md
// layout, so one tree serves both. Existing copies are refreshed in place,
// so a pier upgrade propagates skill fixes. Runs from the wizard's skills
// step and standalone as `pier skills`; returns print-ready progress lines
// and any installation error.
func InstallSkills(home string, agents []string) ([]string, error) {
	var notes []string
	for _, agent := range agents {
		switch changed, err := syncSkills(filepath.Join(home, agent, "skills")); {
		case err != nil:
			return notes, fmt.Errorf("install pier-onboard skill in ~/%s/skills: %w", agent, err)
		case changed:
			notes = append(notes, "  "+ui.OK.Render("+")+" installed the pier-onboard skill "+
				ui.Dim.Render("~/"+agent+"/skills — ask your agent to \"set this repo up for pier\""))
		default:
			notes = append(notes, ui.Dim.Render("  = pier-onboard skill up to date in ~/"+agent+"/skills"))
		}
	}
	return notes, nil
}

// offerSkills is the wizard's skills step: one confirm per detected agent,
// then install. A skills dir confirmed here may postdate manifest detection
// (first run), so it's appended to the session manifest and the config
// re-saved — but only when the user opted into copying agent config at all
// (an emptied manifest means they said no).
func offerSkills(in *bufio.Reader, cfg *config.Config, home string) error {
	agents := AgentDirs(home)
	if len(agents) == 0 {
		return nil
	}
	fmt.Println("\n " + ui.Accent.Render("agent skills") +
		ui.Dim.Render("  pier-onboard teaches an agent to set a repo up for pier"))
	var chosen []string
	for _, agent := range agents {
		if yes(in, "install/refresh for "+strings.TrimPrefix(agent, ".")+" (~/"+agent+"/skills)?", true) {
			chosen = append(chosen, agent)
		}
	}
	if len(chosen) == 0 {
		fmt.Println(ui.Dim.Render("  (skipped — `pier skills` installs them anytime)"))
		return nil
	}
	saved := false
	for _, agent := range chosen {
		notes, installErr := InstallSkills(home, []string{agent})
		for _, n := range notes {
			fmt.Println(n)
		}

		entry := agent + "/skills"
		// Cleanup/index bookkeeping can fail after every bundled file is safely
		// in place. Verify the actual installation before deciding whether the
		// directory belongs in the session manifest.
		installed := installErr == nil || bundledSkillsCurrent(filepath.Join(home, entry))
		if installed && len(cfg.Secrets.Manifest) > 0 && !slices.Contains(cfg.Secrets.Manifest, entry) {
			cfg.Secrets.Manifest = append(cfg.Secrets.Manifest, entry)
			if err := cfg.Save(); err != nil {
				return err
			}
			saved = true
			fmt.Println(ui.Dim.Render("    + ~/" + entry + " added to the session manifest"))
		}
		if installErr != nil {
			return installErr
		}
	}
	if saved {
		fmt.Println(ui.Dim.Render("  the skill travels into sessions with the rest of the agent config"))
	}
	return nil
}

const bundledSkillsIndex = ".pier-bundled-files"

type bundledSkillsIndexData struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// syncSkills refreshes every embedded file and removes paths that an earlier
// bundle owned but the current bundle no longer contains. The ownership index
// leaves untracked, user-created files alone, including additions within a
// bundled skill directory. It reports whether anything was written or removed.
func syncSkills(dst string) (changed bool, err error) {
	current := make(map[string]string)
	contents := make(map[string][]byte)
	var currentFiles []string
	err = fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			content, err := fs.ReadFile(skills.FS, path)
			if err != nil {
				return err
			}
			current[path] = bundledSkillHash(content)
			contents[path] = content
			currentFiles = append(currentFiles, path)
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	previousFiles, err := readBundledSkillsIndex(dst)
	if err != nil {
		return false, err
	}
	for path, previousHash := range previousFiles {
		if _, stillBundled := current[path]; stillBundled {
			continue
		}
		removed, err := removeBundledSkillFile(dst, path, previousHash)
		changed = changed || removed
		if err != nil {
			return changed, err
		}
	}

	for _, path := range currentFiles {
		target := filepath.Join(dst, filepath.FromSlash(path))
		wrote, err := writeBundledSkillFile(dst, target, contents[path])
		if err != nil {
			return changed, err
		}
		changed = changed || wrote
	}

	index, err := marshalBundledSkillsIndex(current)
	if err != nil {
		return changed, err
	}
	indexPath := filepath.Join(dst, bundledSkillsIndex)
	wrote, err := writeBundledSkillFile(dst, indexPath, index)
	if err != nil {
		return changed, err
	}
	return changed || wrote, nil
}

func readBundledSkillsIndex(dst string) (map[string]string, error) {
	indexPath := filepath.Join(dst, bundledSkillsIndex)
	info, err := os.Lstat(indexPath)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("read bundled skills index %s: path is a symlink", indexPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read bundled skills index %s: path is not a regular file", indexPath)
	}
	b, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}

	// The path-only format existed on this PR before hashes were introduced.
	// Preserve those files rather than guessing ownership during migration.
	if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		files := make(map[string]string)
		for _, path := range strings.Split(string(b), "\n") {
			if path == "" {
				continue
			}
			if !validBundledSkillPath(path) {
				return nil, fmt.Errorf("invalid path %q in %s", path, bundledSkillsIndex)
			}
			files[path] = ""
		}
		return files, nil
	}

	var index bundledSkillsIndexData
	if err := json.Unmarshal(b, &index); err != nil {
		return nil, fmt.Errorf("read bundled skills index %s: %w", indexPath, err)
	}
	if index.Version != 1 || index.Files == nil {
		return nil, fmt.Errorf("read bundled skills index %s: unsupported version %d", indexPath, index.Version)
	}
	for path, hash := range index.Files {
		decoded, err := hex.DecodeString(hash)
		if !validBundledSkillPath(path) || err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("invalid path %q in %s", path, bundledSkillsIndex)
		}
	}
	return index.Files, nil
}

func marshalBundledSkillsIndex(files map[string]string) ([]byte, error) {
	b, err := json.MarshalIndent(bundledSkillsIndexData{Version: 1, Files: files}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func validBundledSkillPath(path string) bool {
	return path != "." && path != bundledSkillsIndex && fs.ValidPath(path) && !strings.Contains(path, `\`)
}

func bundledSkillHash(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

// writeBundledSkillFile refuses to follow symlinks at a managed path or in a
// bundled subtree. Writing through either could replace user data outside the
// skills directory. A temporary regular file and rename also make the final
// replacement atomic and prevent a last-component symlink race.
func writeBundledSkillFile(dst, target string, want []byte) (bool, error) {
	if err := ensureBundledSkillParent(dst, filepath.Dir(target)); err != nil {
		return false, err
	}

	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("write bundled skill file %s: path is a symlink", target)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("write bundled skill file %s: path is not a regular file", target)
		}
		have, err := os.ReadFile(target)
		if err != nil {
			return false, err
		}
		if bytes.Equal(have, want) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".pier-skill-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(want); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return false, err
	}
	return true, nil
}

// ensureBundledSkillParent creates directories below dst one at a time so an
// existing symlink cannot redirect a bundled subtree. dst itself may be a
// symlink because users commonly keep their whole agent config elsewhere.
func ensureBundledSkillParent(dst, dir string) error {
	return bundledSkillParent(dst, dir, true)
}

func checkBundledSkillParent(dst, dir string) error {
	return bundledSkillParent(dst, dir, false)
}

func bundledSkillParent(dst, dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
	}
	info, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("skills path %s is not a directory", dst)
	}

	rel, err := filepath.Rel(dst, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("bundled skill directory %s is outside %s", dir, dst)
	}
	current := dst
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if create && os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("write bundled skill directory %s: path is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("write bundled skill directory %s: path is not a directory", current)
		}
	}
	return nil
}

// removeBundledSkillFile removes a formerly bundled path only while its
// contents still match what Pier wrote. Modified, recreated, legacy-indexed,
// and non-regular paths are user-owned and deliberately preserved.
func removeBundledSkillFile(dst, path, previousHash string) (bool, error) {
	if previousHash == "" {
		return false, nil
	}
	target := filepath.Join(dst, filepath.FromSlash(path))
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return false, err
	}
	if bundledSkillHash(content) != previousHash {
		return false, nil
	}
	if err := os.Remove(target); err != nil {
		return false, err
	}
	return true, pruneEmptySkillDirs(filepath.Dir(target), dst)
}

// bundledSkillsCurrent checks usability independently of cleanup metadata.
// The wizard uses it after an error so an already-complete installation is
// still carried into sessions, while an absent or partial installation is not.
func bundledSkillsCurrent(dst string) bool {
	err := fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := fs.ReadFile(skills.FS, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if err := checkBundledSkillParent(dst, filepath.Dir(target)); err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			if err != nil {
				return err
			}
			return fmt.Errorf("bundled skill file %s is not regular", target)
		}
		have, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if !bytes.Equal(have, want) {
			return fmt.Errorf("bundled skill file %s is not current", target)
		}
		return nil
	})
	return err == nil
}

func pruneEmptySkillDirs(dir, stop string) error {
	for dir != stop {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			dir = filepath.Dir(dir)
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

package wizard

import (
	"bufio"
	"bytes"
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
		notes, err := InstallSkills(home, []string{agent})
		for _, n := range notes {
			fmt.Println(n)
		}
		if err != nil {
			return err
		}

		entry := agent + "/skills"
		if len(cfg.Secrets.Manifest) > 0 && !slices.Contains(cfg.Secrets.Manifest, entry) {
			cfg.Secrets.Manifest = append(cfg.Secrets.Manifest, entry)
			if err := cfg.Save(); err != nil {
				return err
			}
			saved = true
			fmt.Println(ui.Dim.Render("    + ~/" + entry + " added to the session manifest"))
		}
	}
	if saved {
		fmt.Println(ui.Dim.Render("  the skill travels into sessions with the rest of the agent config"))
	}
	return nil
}

const bundledSkillsIndex = ".pier-bundled-files"

// syncSkills refreshes every embedded file and removes paths that an earlier
// bundle owned but the current bundle no longer contains. The ownership index
// leaves untracked, user-created files alone, including additions within a
// bundled skill directory. It reports whether anything was written or removed.
func syncSkills(dst string) (changed bool, err error) {
	current := make(map[string]struct{})
	var currentFiles []string
	err = fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			current[path] = struct{}{}
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
	for _, path := range previousFiles {
		if _, stillBundled := current[path]; stillBundled {
			continue
		}
		removed, err := removeBundledSkillFile(dst, path)
		changed = changed || removed
		if err != nil {
			return changed, err
		}
	}

	err = fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := fs.ReadFile(skills.FS, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		wrote, err := writeBundledSkillFile(dst, target, want)
		if err != nil {
			return err
		}
		changed = changed || wrote
		return nil
	})
	if err != nil {
		return changed, err
	}

	index := []byte(strings.Join(currentFiles, "\n") + "\n")
	indexPath := filepath.Join(dst, bundledSkillsIndex)
	wrote, err := writeBundledSkillFile(dst, indexPath, index)
	if err != nil {
		return changed, err
	}
	return changed || wrote, nil
}

func readBundledSkillsIndex(dst string) ([]string, error) {
	indexPath := filepath.Join(dst, bundledSkillsIndex)
	info, err := os.Lstat(indexPath)
	if os.IsNotExist(err) {
		return nil, nil
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

	var paths []string
	for _, path := range strings.Split(string(b), "\n") {
		if path == "" {
			continue
		}
		if path == "." || path == bundledSkillsIndex || !fs.ValidPath(path) || strings.Contains(path, `\`) {
			return nil, fmt.Errorf("invalid path %q in %s", path, bundledSkillsIndex)
		}
		paths = append(paths, path)
	}
	return paths, nil
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
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
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
		if os.IsNotExist(err) {
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

// removeBundledSkillFile removes one path claimed by the previous ownership
// index, then prunes only empty parent directories. A directory where the
// index promised a file is left intact because it may contain user data.
func removeBundledSkillFile(dst, path string) (bool, error) {
	target := filepath.Join(dst, filepath.FromSlash(path))
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("remove obsolete bundled file %s: path is now a directory", target)
	}
	if err := os.Remove(target); err != nil {
		return false, err
	}
	return true, pruneEmptySkillDirs(filepath.Dir(target), dst)
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

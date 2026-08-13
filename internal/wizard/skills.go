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
// step and standalone as `pier skills`; returns print-ready progress lines.
func InstallSkills(home string, agents []string) []string {
	var notes []string
	for _, agent := range agents {
		switch changed, err := syncSkills(filepath.Join(home, agent, "skills")); {
		case err != nil:
			notes = append(notes, "  "+ui.Mark(false)+" pier-onboard skill: "+err.Error())
		case changed:
			notes = append(notes, "  "+ui.OK.Render("+")+" installed the pier-onboard skill "+
				ui.Dim.Render("~/"+agent+"/skills — ask your agent to \"set this repo up for pier\""))
		default:
			notes = append(notes, ui.Dim.Render("  = pier-onboard skill up to date in ~/"+agent+"/skills"))
		}
	}
	return notes
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
	for _, n := range InstallSkills(home, chosen) {
		fmt.Println(n)
	}
	saved := false
	for _, agent := range chosen {
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

// syncSkills writes every embedded skill file under dst, skipping files
// already at the embedded content, and reports whether anything was written.
func syncSkills(dst string) (changed bool, err error) {
	err = fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := fs.ReadFile(skills.FS, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if have, err := os.ReadFile(target); err == nil && bytes.Equal(have, want) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, want, 0o644); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

package wizard

import (
	"bufio"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

// InstallSkills lays the bundled skills (skills.FS — notably pier-onboard,
// how an agent learns to write a repo's .pier files) into each named agent's
// user skills directory. Claude Code and Codex read the same SKILL.md
// layout, so one tree serves both. Existing copies are refreshed in place,
// so a pier upgrade propagates skill fixes. Runs from the wizard's skills
// step and standalone as `pier setup --skills`; returns print-ready progress lines
// and any installation error.
func InstallSkills(home string, agents []string) ([]string, error) {
	var notes []string
	for _, agent := range agents {
		switch changed, err := pier.SyncSkills(filepath.Join(home, agent, "skills")); {
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
	agents := pier.AgentDirs(home)
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
		fmt.Println(ui.Dim.Render("  (skipped — `pier setup --skills` installs them anytime)"))
		return nil
	}
	return installChosen(cfg, home, chosen)
}

// installChosen installs the bundled skills for the chosen agents and, when
// the user copies agent config into sessions at all, adds each installed
// skills dir to the session manifest so the skill travels too.
func installChosen(cfg *config.Config, home string, chosen []string) error {
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
		installed := installErr == nil || pier.SkillsCurrent(filepath.Join(home, entry))
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

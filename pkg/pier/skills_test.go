package pier

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSyncSkills covers the lifecycle: no agents → nothing to do,
// claude only → claude gets the skill, re-run → no rewrite, stale copy →
// refreshed, obsolete owned file → removed, user file → preserved, codex
// appears → codex gets it too.
func TestSyncSkills(t *testing.T) {
	home := t.TempDir()
	// install syncs every agent here and reports, per agent, whether
	// anything changed.
	install := func() []string {
		t.Helper()
		var notes []string
		for _, agent := range AgentDirs(home) {
			changed, err := SyncSkills(filepath.Join(home, agent, "skills"))
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				notes = append(notes, "installed "+agent)
			} else {
				notes = append(notes, "up to date "+agent)
			}
		}
		return notes
	}

	if dirs := AgentDirs(home); len(dirs) != 0 {
		t.Fatalf("no agents on the machine, got %q", dirs)
	}

	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if dirs := AgentDirs(home); !slices.Equal(dirs, []string{".claude"}) {
		t.Fatalf("want just .claude, got %q", dirs)
	}
	notes := install()
	if len(notes) != 1 || !strings.Contains(notes[0], "installed") {
		t.Fatalf("want one install note, got %q", notes)
	}
	claudeSkill := filepath.Join(home, ".claude/skills/pier-onboard/SKILL.md")
	b, err := os.ReadFile(claudeSkill)
	if err != nil {
		t.Fatal(err)
	}
	// The frontmatter both Claude Code and Codex require, with the name
	// matching the directory.
	for _, want := range []string{"name: pier-onboard", "description:"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("SKILL.md missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatal(".codex was conjured for a machine without codex")
	}

	if notes := install(); len(notes) != 1 || !strings.Contains(notes[0], "up to date") {
		t.Fatalf("second run should be a no-op, got %q", notes)
	}

	if err := os.WriteFile(claudeSkill, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if notes := install(); len(notes) != 1 || !strings.Contains(notes[0], "installed") {
		t.Fatalf("stale copy should refresh, got %q", notes)
	}
	if b, _ := os.ReadFile(claudeSkill); string(b) == "stale" {
		t.Fatal("stale skill was not refreshed")
	}

	obsolete := filepath.Join(home, ".claude/skills/pier-onboard/obsolete.md")
	obsoleteContent := []byte("removed from a later bundle")
	if err := os.WriteFile(obsolete, obsoleteContent, 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(home, ".claude/skills", bundledSkillsIndex)
	index, err := readBundledSkillsIndex(filepath.Dir(indexPath))
	if err != nil {
		t.Fatal(err)
	}
	index["pier-onboard/obsolete.md"] = bundledSkillHash(obsoleteContent)
	replacement := filepath.Join(home, ".claude/skills/pier-onboard/replaced.md")
	index["pier-onboard/replaced.md"] = bundledSkillHash([]byte("old bundled content"))
	if err := os.WriteFile(replacement, []byte("user replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	encodedIndex, err := marshalBundledSkillsIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, encodedIndex, 0o644); err != nil {
		t.Fatal(err)
	}
	customReference := filepath.Join(home, ".claude/skills/pier-onboard/custom.md")
	if err := os.WriteFile(customReference, []byte("user managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	customSkill := filepath.Join(home, ".claude/skills/custom/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(customSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customSkill, []byte("user managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if notes := install(); len(notes) != 1 || !strings.Contains(notes[0], "installed") {
		t.Fatalf("obsolete bundled file should be removed, got %q", notes)
	}
	if _, err := os.Stat(obsolete); !os.IsNotExist(err) {
		t.Fatalf("obsolete bundled file remains: %v", err)
	}
	if b, err := os.ReadFile(replacement); err != nil || string(b) != "user replacement" {
		t.Fatalf("user replacement of obsolete file changed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(customReference); err != nil || string(b) != "user managed" {
		t.Fatalf("user reference inside bundled skill changed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(customSkill); err != nil || string(b) != "user managed" {
		t.Fatalf("sibling user skill changed: %q, %v", b, err)
	}

	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if notes := install(); len(notes) != 2 {
		t.Fatalf("want claude+codex notes, got %q", notes)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex/skills/pier-onboard/SKILL.md")); err != nil {
		t.Fatalf("codex skill missing: %v", err)
	}
}

package wizard

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/config"
)

// TestInstallSkills covers the lifecycle: no agents → nothing to do,
// claude only → claude gets the skill, re-run → no rewrite, stale copy →
// refreshed, obsolete bundled file → removed, codex appears → codex gets it too.
func TestInstallSkills(t *testing.T) {
	home := t.TempDir()
	install := func() []string {
		t.Helper()
		notes, err := InstallSkills(home, AgentDirs(home))
		if err != nil {
			t.Fatal(err)
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
	if err := os.WriteFile(obsolete, []byte("removed from a later bundle"), 0o644); err != nil {
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

func TestInstallSkillsReturnsErrorsBeforeManifestSave(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude/skills"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	notes, err := InstallSkills(home, []string{".claude"})
	if err == nil {
		t.Fatal("install into an invalid skills path succeeded")
	}
	if len(notes) != 0 {
		t.Fatalf("failed install returned success notes: %q", notes)
	}

	cfg := config.Default()
	cfg.Secrets.Manifest = []string{".claude/settings.json"}
	err = offerSkills(bufio.NewReader(strings.NewReader("y\n")), &cfg, home)
	if err == nil {
		t.Fatal("wizard ignored skill installation failure")
	}
	if slices.Contains(cfg.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("failed skill destination entered manifest: %q", cfg.Secrets.Manifest)
	}
}

// TestOfferSkills drives the wizard step through scripted stdin: per-agent
// confirms gate the install, and a confirmed skills dir joins the session
// manifest only when the user opted into copying agent config at all.
func TestOfferSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // cfg.Save writes under $HOME
	for _, agent := range []string{".claude", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, agent), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// claude yes, codex no; manifest already opted-in.
	cfg := config.Default()
	cfg.Secrets.Manifest = []string{".claude/settings.json"}
	if err := offerSkills(bufio.NewReader(strings.NewReader("y\nn\n")), &cfg, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/pier-onboard/SKILL.md")); err != nil {
		t.Fatalf("claude skill missing after yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex/skills")); !os.IsNotExist(err) {
		t.Fatal("codex skills installed despite no")
	}
	if !slices.Contains(cfg.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("confirmed skills dir not in manifest: %q", cfg.Secrets.Manifest)
	}
	if slices.Contains(cfg.Secrets.Manifest, ".codex/skills") {
		t.Fatalf("declined agent entered the manifest: %q", cfg.Secrets.Manifest)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(saved.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("manifest append was not saved: %q", saved.Secrets.Manifest)
	}

	// Empty manifest = the user said no to copying agent config; installing
	// the skill locally must not sneak entries in.
	cfg2 := config.Default()
	cfg2.Secrets.Manifest = nil
	if err := offerSkills(bufio.NewReader(strings.NewReader("y\ny\n")), &cfg2, home); err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Secrets.Manifest) != 0 {
		t.Fatalf("opted-out manifest gained entries: %q", cfg2.Secrets.Manifest)
	}
}

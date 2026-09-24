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
// refreshed, obsolete owned file → removed, user file → preserved, codex
// appears → codex gets it too.
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

func TestInstallSkillsRefusesSymlinkedBundledFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkills(home, []string{".claude"}); err != nil {
		t.Fatal(err)
	}

	skill := filepath.Join(home, ".claude/skills/pier-onboard/SKILL.md")
	external := filepath.Join(home, "user-managed.md")
	if err := os.WriteFile(external, []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, skill); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallSkills(home, []string{".claude"}); err == nil {
		t.Fatal("refresh followed a symlinked bundled file")
	}
	if b, err := os.ReadFile(external); err != nil || string(b) != "do not overwrite" {
		t.Fatalf("external symlink target changed: %q, %v", b, err)
	}
	if info, err := os.Lstat(skill); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("user-managed symlink changed: %v, %v", info, err)
	}
}

func TestOfferSkillsPersistsSuccessBeforeLaterFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, agent := range []string{".claude", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, agent), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".codex/skills"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Secrets.Manifest = []string{".claude/settings.json"}
	err := offerSkills(bufio.NewReader(strings.NewReader("y\ny\n")), &cfg, home)
	if err == nil {
		t.Fatal("wizard ignored second skill installation failure")
	}
	if !slices.Contains(cfg.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("successful first install missing from manifest: %q", cfg.Secrets.Manifest)
	}
	if slices.Contains(cfg.Secrets.Manifest, ".codex/skills") {
		t.Fatalf("failed second install entered manifest: %q", cfg.Secrets.Manifest)
	}
	saved, loadErr := config.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !slices.Contains(saved.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("successful first install was not saved: %q", saved.Secrets.Manifest)
	}
}

func TestOfferSkillsPersistsCurrentSkillWhenIndexFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkills(home, []string{".claude"}); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(home, ".claude/skills", bundledSkillsIndex)
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(indexPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Secrets.Manifest = []string{".claude/settings.json"}
	err := offerSkills(bufio.NewReader(strings.NewReader("y\n")), &cfg, home)
	if err == nil {
		t.Fatal("wizard ignored ownership index failure")
	}
	if !slices.Contains(cfg.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("current skill missing from manifest after index failure: %q", cfg.Secrets.Manifest)
	}
	saved, loadErr := config.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !slices.Contains(saved.Secrets.Manifest, ".claude/skills") {
		t.Fatalf("current skill was not saved after index failure: %q", saved.Secrets.Manifest)
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

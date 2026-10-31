package wizard

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

// TestInstallSkills covers the notes the terminal shows: installed, then
// up to date, and one line per agent once a second one appears. The sync
// itself is pier's (pkg/pier TestSyncSkills).
func TestInstallSkills(t *testing.T) {
	home := t.TempDir()
	install := func() []string {
		t.Helper()
		notes, err := InstallSkills(home, pier.AgentDirs(home))
		if err != nil {
			t.Fatal(err)
		}
		return notes
	}
	if notes := install(); len(notes) != 0 {
		t.Fatalf("no agents on the machine, got %q", notes)
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if notes := install(); len(notes) != 1 || !strings.Contains(notes[0], "installed") {
		t.Fatalf("want one install note, got %q", notes)
	}
	if notes := install(); len(notes) != 1 || !strings.Contains(notes[0], "up to date") {
		t.Fatalf("second run should be a no-op, got %q", notes)
	}
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if notes := install(); len(notes) != 2 {
		t.Fatalf("want claude+codex notes, got %q", notes)
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
	indexPath := filepath.Join(home, ".claude/skills", ".pier-bundled-files")
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

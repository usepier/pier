package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrompts(t *testing.T) {
	var c Config
	if c.Prompt("shop", "createPR") != defaultPrompt("createPR") {
		t.Error("an unset prompt is pier's")
	}
	if err := c.SetPrompt("", "fixCI", "Fix PR #{pr} please"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPrompt("shop", "fixCI", "  shop's own  "); err != nil {
		t.Fatal(err)
	}
	if got := c.Prompt("shop", "fixCI"); got != "shop's own" {
		t.Errorf("repo = %q", got)
	}
	if got := FillPrompt(c.Prompt("web", "fixCI"), 7); got != "Fix PR #7 please" {
		t.Errorf("global = %q", got)
	}
	if c.SetPrompt("shop", "nope", "x") == nil || c.SetPrompt("../x", "fixCI", "x") == nil {
		t.Error("bad keys or repos are refused")
	}
	// Back to falling through, and nothing left behind for the repo.
	c.SetPrompt("shop", "fixCI", "")
	if _, ok := c.Prompts.Repos["shop"]; ok || c.Prompt("shop", "fixCI") != "Fix PR #{pr} please" {
		t.Errorf("reset = %+v", c.Prompts)
	}

	// They round-trip through the file, under [prompts].
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	c.SetPrompt("shop", "review", "Look at these:")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(Dir(), "config.toml"))
	if !strings.Contains(string(b), "[prompts]") || !strings.Contains(string(b), "[prompts.repos.shop]") {
		t.Errorf("file = %s", b)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.Prompt("shop", "review") != "Look at these:" || back.Prompt("x", "fixCI") != "Fix PR #{pr} please" {
		t.Errorf("loaded = %+v", back.Prompts)
	}
}

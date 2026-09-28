package ui

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := Tilde(filepath.Join(home, ".config", "pier", "logs", "create-x.log")); got != "~/.config/pier/logs/create-x.log" {
		t.Errorf("home-prefixed: got %q", got)
	}
	// Not under home, and home-without-separator (a sibling like /home/uX):
	// both stay untouched.
	if got := Tilde("/var/log/system.log"); got != "/var/log/system.log" {
		t.Errorf("outside home: got %q", got)
	}
	if got := Tilde(home + "x/file"); got != home+"x/file" {
		t.Errorf("sibling prefix: got %q", got)
	}
}

// The picker's names and the colors must agree, in the documented order.
func TestThemesAreComplete(t *testing.T) {
	want := []string{"teal", "navy", "violet", "emerald", "orange", "crimson", "pink", "amber", "graphite"}
	if strings.Join(ThemeNames, ",") != strings.Join(want, ",") {
		t.Errorf("theme order: got %v", ThemeNames)
	}
	for _, n := range ThemeNames {
		if Themes[n] == nil {
			t.Errorf("no color for %s", n)
		}
	}
}

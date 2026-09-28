// Package ui is pier's shared terminal look: one accent, ANSI-palette colors
// (they follow the user's terminal theme), lots of dim. lipgloss degrades
// everything to plain text when stdout isn't a TTY, so piped output stays
// clean.
package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	Accent = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // teal — nautical
	Title  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	Bold   = lipgloss.NewStyle().Bold(true)
	Dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	OK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	Warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	Bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	Strain = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange

	// Box wraps input areas (the claude-code look).
	Box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		MarginLeft(1)
)

// ThemeNames lists the accent colors in the order the settings picker shows
// them. Teal is the terminal's own cyan (it follows the user's palette); the
// rest are fixed tones.
var ThemeNames = []string{"teal", "navy", "violet", "emerald", "orange", "crimson", "pink", "amber", "graphite"}

// Themes maps each accent color's name to its color.
var Themes = map[string]lipgloss.TerminalColor{
	"teal":     lipgloss.Color("6"),
	"navy":     lipgloss.Color("#2B4FC5"),
	"violet":   lipgloss.Color("#7C3AED"),
	"emerald":  lipgloss.Color("#10B981"),
	"orange":   lipgloss.Color("#F97316"),
	"crimson":  lipgloss.Color("#E11D48"),
	"pink":     lipgloss.Color("#EC4899"),
	"amber":    lipgloss.Color("#F59E0B"),
	"graphite": lipgloss.Color("#A1A1AA"),
}

// darkThemes need light text where the accent is a background (the active
// tab); black on them is unreadable.
var darkThemes = map[string]bool{"navy": true, "violet": true, "crimson": true}

// OnAccent is the text color for text drawn on the accent.
var OnAccent lipgloss.TerminalColor = lipgloss.Color("0")

// AccentColor is the active accent; SetAccent changes it.
var AccentColor lipgloss.TerminalColor = lipgloss.Color("6")

// SetAccent switches the accent to a theme by name (unknown names keep the
// default) and restyles the shared accent styles.
func SetAccent(theme string) {
	c, ok := Themes[theme]
	if !ok {
		c = Themes["teal"]
	}
	AccentColor = c
	OnAccent = lipgloss.Color("0")
	if darkThemes[theme] {
		OnAccent = lipgloss.Color("15")
	}
	Accent = lipgloss.NewStyle().Foreground(c)
	Title = lipgloss.NewStyle().Foreground(c).Bold(true)
}

// Step renders a progress-step line: accent chevron, indented under the
// command's bold header — the shared shape for every long-running command
// (create, bake, resize).
func Step(s string) string {
	return Accent.Render("  ▸") + " " + s
}

// Mark renders a green ✓ or red ✗.
func Mark(ok bool) string {
	if ok {
		return OK.Render("✓")
	}
	return Bad.Render("✗")
}

// Tilde shortens a path under the user's home to ~/... — for display only,
// never for opening. Absolute paths overflow status lines and put the local
// username in every screenshot someone shares.
func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return p
}

// Keys renders a footer hint from key/description pairs:
// Keys("enter", "attach", "q", "quit") → "enter attach · q quit".
func Keys(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, Accent.Render(pairs[i])+" "+Dim.Render(pairs[i+1]))
	}
	return strings.Join(parts, Dim.Render(" · "))
}

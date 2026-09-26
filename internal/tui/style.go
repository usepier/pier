package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/usepier/pier/pkg/pier"
)

// The look: ANSI palette colors, so pier follows the user's terminal theme;
// one teal accent; titled panels with the title set into the border (btop);
// a soft highlight bar for the selected row (opencode); a colored state dot
// per session (herdr). Everything else stays quiet.
var (
	cAccent = lipgloss.Color("6")
	cDim    = lipgloss.Color("8")
	cFaint  = lipgloss.Color("7")
	cOK     = lipgloss.Color("2")
	cWarn   = lipgloss.Color("3")
	cBad    = lipgloss.Color("1")
	cStrain = lipgloss.Color("208")
	cSelBg  = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}

	sAccent = lipgloss.NewStyle().Foreground(cAccent)
	sBold   = lipgloss.NewStyle().Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sBad    = lipgloss.NewStyle().Foreground(cBad)
	sStrain = lipgloss.NewStyle().Foreground(cStrain)
	sBrand  = lipgloss.NewStyle().Foreground(cAccent).Bold(true)

	sTabOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(cAccent).Bold(true).Padding(0, 1)
	sTabOff = lipgloss.NewStyle().Foreground(cFaint).Padding(0, 1)
	sSel    = lipgloss.NewStyle().Background(cSelBg).Bold(true)
	sKey    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

// panel draws a rounded box w wide and h tall with title set into the top
// border and an optional right-aligned note beside it. Lines are clipped to
// the inner width and padded; missing lines render blank. A focused panel
// draws its border in the accent.
func panel(title, note string, lines []string, w, h int, focused bool) string {
	if w < 8 {
		w = 8
	}
	if h < 3 {
		h = 3
	}
	bc := sDim
	tc := sFaint.Bold(true)
	if focused {
		bc = sAccent
		tc = sBrand
	}
	inner := w - 2
	content := inner - 2

	head := bc.Render("─ ") + tc.Render(title) + " "
	if note != "" {
		n := " " + sDim.Render(note) + " "
		pad := inner - lipgloss.Width(head) - lipgloss.Width(n) - 1
		if pad < 1 {
			n, pad = "", inner-lipgloss.Width(head)
		}
		head += bc.Render(strings.Repeat("─", max(pad, 0))) + n + bc.Render("─")
	} else {
		head += bc.Render(strings.Repeat("─", max(inner-lipgloss.Width(head), 0)))
	}
	head = ansi.Truncate(head, inner, "")
	var b strings.Builder
	b.WriteString(bc.Render("╭") + head + bc.Render("╮") + "\n")
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(bc.Render("│") + " " + fit(l, content) + " " + bc.Render("│") + "\n")
	}
	b.WriteString(bc.Render("╰" + strings.Repeat("─", inner) + "╯"))
	return b.String()
}

// fit clips s to w visible cells and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// selected renders a row as the highlight bar: styles stripped (their
// resets would punch holes in the background), full width.
func selected(s string, w int) string {
	return sSel.Render(fit(ansi.Strip(s), w))
}

// keys renders a hint strip: keys("enter", "attach", "q", "quit").
func keys(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+" "+sDim.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// badge is a session's state as a colored dot and word, with the
// supervisor's strain and setup flags appended.
func badge(s pier.Session, frame int) (dot, word string) {
	switch s.State {
	case pier.StateWorking:
		dot, word = sOK.Render("●"), sOK.Render("working")
	case pier.StateRunning:
		dot, word = sAccent.Render("●"), sAccent.Render("attached")
	case pier.StateIdle:
		dot, word = sWarn.Render("●"), sWarn.Render("idle")
	case pier.StateCreating:
		dot, word = sAccent.Render(spinFrames[frame%len(spinFrames)]), sAccent.Render("creating")
	case pier.StateParked:
		dot, word = sDim.Render("○"), sDim.Render("parked")
	case pier.StateDeleting:
		dot, word = sDim.Render("◌"), sDim.Render("deleting")
	case pier.StateFailed:
		dot, word = sBad.Render("✗"), sBad.Render("create failed")
	default:
		dot, word = sBad.Render("✗"), sBad.Render(string(s.State))
	}
	if s.Strained {
		word += sStrain.Render(" · strained")
	}
	switch s.Setup {
	case "running":
		word += sAccent.Render(" · setup")
	case "failed":
		word += sBad.Render(" · setup failed")
	}
	return dot, word
}

// field renders a detail row: a dim fixed-width label, then the value.
func field(label, value string) string {
	return sDim.Render(fit(label, 10)) + value
}

func ago(t time.Time) string {
	a := pier.Age(t, time.Now())
	if a == "now" || a == "-" {
		return a
	}
	return a + " ago"
}

// wrap breaks plain text into lines of at most w cells, on spaces.
func wrap(text string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) > w:
				out = append(out, line)
				line = word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usepier/pier/internal/ui"
)

// The l key: the session's setup log as a pager inside the TUI — scroll,
// live-follow while the setup still runs, esc back to the list. The CLI
// `pier logs` stays the raw pipeable path; this view answers "what is it
// doing" without leaving the screen.

type logMsg struct {
	name string
	text string
	err  error
}

type logPollMsg struct{}

// logPollCmd refetches on a slow cadence while the viewer is open, so a
// running setup streams in place. One ssh exec per tick over the tunnel —
// the same weight as the list's beacon probe.
func logPollCmd() tea.Cmd {
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return logPollMsg{} })
}

func (m model) fetchLog() tea.Cmd {
	s := m.logSess
	fetch := m.opts.FetchLog
	return func() tea.Msg {
		text, err := fetch(s)
		return logMsg{s.Name, text, err}
	}
}

func (m model) updateLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q", "esc":
		m.mode = modeList
		m.status = ""
	case "up", "k":
		m.logOff = max(0, m.logOff-1)
		m.logStick = false
	case "down", "j":
		m.logOff = min(m.maxLogOff(), m.logOff+1)
		m.logStick = m.logOff == m.maxLogOff()
	case "pgup", "b":
		m.logOff = max(0, m.logOff-m.logBodyH())
		m.logStick = false
	case "pgdown", " ":
		m.logOff = min(m.maxLogOff(), m.logOff+m.logBodyH())
		m.logStick = m.logOff == m.maxLogOff()
	case "g":
		m.logOff, m.logStick = 0, false
	case "G":
		m.logOff, m.logStick = m.maxLogOff(), true
	case "r":
		if !m.logLoading {
			m.logLoading = true
			return m, m.fetchLog()
		}
	}
	return m, nil
}

// setLogText replaces the viewer content: sanitize once, rewrap to the
// current width, and keep the view pinned to the tail while following.
func (m *model) setLogText(text string) {
	m.logText = sanitizeLog(text)
	m.relayoutLog()
}

func (m *model) relayoutLog() {
	m.logLines = wrapLines(m.logText, m.logWidth())
	if m.logStick || m.logOff > m.maxLogOff() {
		m.logOff = m.maxLogOff()
	}
}

func (m model) logWidth() int {
	if m.width >= 12 {
		return m.width - 4
	}
	return 96
}

func (m model) logBodyH() int {
	if m.height >= 12 {
		return m.height - 6
	}
	return 20
}

func (m model) maxLogOff() int {
	return max(0, len(m.logLines)-m.logBodyH())
}

func (m model) logsView() string {
	var b strings.Builder
	head := " " + ui.Title.Render("⚓ setup log") + ui.Dim.Render("  "+m.logSess.Name)
	if m.logLoading && m.logText == "" {
		head += ui.Dim.Render("  fetching…")
	}
	b.WriteString("\n" + head + "\n\n")

	h := m.logBodyH()
	end := min(len(m.logLines), m.logOff+h)
	for _, line := range m.logLines[m.logOff:end] {
		b.WriteString("  " + styleLogLine(line) + "\n")
	}
	if len(m.logLines) == 0 && !m.logLoading {
		b.WriteString(ui.Dim.Render("  (empty log)") + "\n")
	}
	b.WriteString("\n")

	if m.status != "" && m.statusBad {
		b.WriteString(" " + ui.Bad.Render("! "+m.status) + "\n")
	}
	info := fmt.Sprintf("%d lines", len(m.logLines))
	if m.maxLogOff() > 0 {
		info = fmt.Sprintf("lines %d-%d of %d", m.logOff+1, end, len(m.logLines))
	}
	if m.logStick {
		info += " · following"
	}
	b.WriteString(" " + ui.Dim.Render(info) + "\n")
	b.WriteString(" " + ui.Keys("↑/↓", "scroll", "g/G", "top/end", "r", "refresh", "esc", "back") + "\n")
	return b.String()
}

// styleLogLine keeps the body plain and lets pier's own outcome markers
// carry the only color, so a done/FAILED tail reads at a glance.
func styleLogLine(l string) string {
	switch {
	case strings.HasPrefix(l, "pier setup: FAILED"):
		return ui.Bad.Render(l)
	case strings.HasPrefix(l, "pier setup: done"):
		return ui.OK.Render(l)
	}
	return l
}

// ansiRe matches color/cursor CSI sequences, OSC titles, and stray escapes.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[@-_]")

// sanitizeLog makes a setup log renderable inside a frame: package managers
// and docker draw progress meters with ANSI codes and \r redraws that would
// corrupt the layout — strip the codes, keep only each line's final state,
// drop remaining control characters.
func sanitizeLog(text string) string {
	text = ansiRe.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, l)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// wrapLines hard-wraps to the viewer width so long package-manager lines
// scroll instead of breaking the frame.
func wrapLines(text string, width int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		r := []rune(l)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

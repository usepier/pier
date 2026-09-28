package tui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usepier/pier/pkg/pier"
)

// The l key: a session's setup log inside the app — scroll, live-follow
// while setup still runs, esc back. `pier logs` stays the raw pipeable path.

type logView struct {
	sess    pier.Session
	text    string   // sanitized; the source for rewrapping
	lines   []string // text wrapped to the viewer width
	off     int      // first visible line
	stick   bool     // pinned to the tail: new content keeps the end in view
	loading bool
	polling bool
}

type logMsg struct {
	name string
	text string
	err  error
}

type logPollMsg struct{}

// logPollCmd refetches on a slow cadence while the viewer is open, so a
// running setup streams in place — one ssh exec per tick, the same weight
// as the list's beacon read.
func logPollCmd() tea.Cmd {
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return logPollMsg{} })
}

func (m model) openLogs(s pier.Session) (tea.Model, tea.Cmd) {
	if s.State != pier.StateFailed {
		if err := pier.CheckReady(s); err != nil {
			m.fail(err)
			return m, nil
		}
		if s.State == pier.StateParked {
			m.note(s.Name + " is parked — attach to resume it, or `pier logs " + s.Name + "`")
			return m, nil
		}
	}
	m.ov = ovLogs
	m.log = logView{sess: s, stick: true, loading: true}
	return m, m.fetchLog()
}

func (m model) fetchLog() tea.Cmd {
	s, be := m.log.sess, m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The viewer refetches every few seconds, so the transfer stays
		// bounded; `pier logs` prints everything.
		text, err := be.SetupLog(ctx, s, 2000)
		return logMsg{s.Name, text, err}
	}
}

func (m model) onLog(msg logMsg) (tea.Model, tea.Cmd) {
	if m.ov != ovLogs || msg.name != m.log.sess.Name {
		return m, nil
	}
	m.log.loading = false
	if msg.err != nil {
		m.fail(msg.err)
	} else {
		m.log.text = sanitizeLog(msg.text)
		m.log.relayout(m.logWidth())
		if m.log.stick {
			m.log.off = m.log.maxOff(m.logBodyH())
		}
	}
	// Keep following while the log can still grow.
	if m.log.sess.State != pier.StateFailed && !m.log.polling {
		m.log.polling = true
		return m, logPollCmd()
	}
	return m, nil
}

func (m model) keyLogs(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.logBodyH()
	switch k.String() {
	case "esc", "q", "l":
		m.ov = ovNone
		m.log.polling = false
	case "up", "k":
		m.log.off = max(m.log.off-1, 0)
		m.log.stick = false
	case "down", "j":
		m.log.off = min(m.log.off+1, m.log.maxOff(h))
		m.log.stick = m.log.off == m.log.maxOff(h)
	case "pgup", "b":
		m.log.off = max(m.log.off-h, 0)
		m.log.stick = false
	case "pgdown", " ", "f":
		m.log.off = min(m.log.off+h, m.log.maxOff(h))
		m.log.stick = m.log.off == m.log.maxOff(h)
	case "g", "home":
		m.log.off, m.log.stick = 0, false
	case "G", "end":
		m.log.off, m.log.stick = m.log.maxOff(h), true
	}
	return m, nil
}

func (l *logView) relayout(width int) {
	l.lines = wrapLines(l.text, width)
}

func (l logView) maxOff(h int) int { return max(len(l.lines)-h, 0) }

// logWidth / logBodyH are the viewer panel's inner size.
func (m model) logWidth() int { return max(m.width-4, 20) }
func (m model) logBodyH() int { return max(m.bodyHeight()-2, 3) }

func (m model) logsView() string {
	h := m.logBodyH()
	var lines []string
	switch {
	case m.log.loading && len(m.log.lines) == 0:
		lines = []string{sDim.Render("fetching the log…")}
	case len(m.log.lines) == 0:
		lines = []string{sDim.Render("the log is empty so far")}
	default:
		end := min(m.log.off+h, len(m.log.lines))
		for _, l := range m.log.lines[m.log.off:end] {
			lines = append(lines, styleLogLine(l))
		}
	}
	note := "following"
	if !m.log.stick {
		note = pct(m.log.off+h, len(m.log.lines))
	}
	if m.log.sess.State == pier.StateFailed {
		note = "create log"
	}
	return panel("setup log · "+m.log.sess.Name, note, lines, m.width, m.bodyHeight(), true)
}

func pct(seen, total int) string {
	if total == 0 {
		return ""
	}
	return itoa(min(seen, total)*100/total) + "%"
}

// styleLogLine keeps the body plain and lets pier's own outcome markers
// carry the only color, so a done/FAILED tail reads at a glance.
func styleLogLine(l string) string {
	switch {
	case strings.HasPrefix(l, "pier setup: FAILED"):
		return sBad.Render(l)
	case strings.HasPrefix(l, "pier setup: done"):
		return sOK.Render(l)
	case strings.HasPrefix(l, "==>"):
		return sAccent.Render(l)
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

// tilde shortens a path under home to ~/… for display.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return p
}

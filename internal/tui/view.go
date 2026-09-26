package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/tombstone"
	"github.com/usepier/pier/pkg/pier"
)

// Screen: a one-line header (brand, tabs, live cost), the tab's panels, then
// a status line and the key hints. Wide terminals put the detail panel
// beside the list; narrow ones stack it underneath.

func (m model) View() string {
	if m.width == 0 {
		return "" // first frame, before the size arrives
	}
	var body string
	switch {
	case m.ov == ovLogs:
		body = m.logsView()
	case m.ov == ovHelp:
		body = m.helpView()
	case m.ov == ovResize:
		body = m.resizeView()
	case m.ov == ovPick:
		body = m.pickView()
	default:
		switch m.tab {
		case tabSessions:
			body = m.sessionsView()
		case tabRepos:
			body = m.reposView()
		default:
			body = m.settingsView()
		}
	}
	return m.header() + "\n" + body + "\n" + m.sheet() + m.footer()
}

// bodyHeight is what the panels get: the screen minus header, status and
// keys, and minus any bottom sheet.
func (m model) bodyHeight() int {
	h := m.height - 3
	if m.ov == ovNew || m.ov == ovConfirm || m.ov == ovEdit {
		h -= 3
	}
	return max(h, 6)
}

func (m model) header() string {
	left := " " + sBrand.Render("⚓ pier") + "  "
	for i, name := range tabNames {
		label := name
		if tab(i) == tabRepos {
			if n := m.reminderCount(); n > 0 {
				label += " " + sWarn.Render("•")
			}
		}
		if tab(i) == m.tab {
			left += sTabOn.Render(name)
			if label != name {
				left += sWarn.Render("•")
			}
		} else {
			left += sTabOff.Render(label)
		}
	}

	var meta []string
	if m.loading {
		meta = append(meta, sAccent.Render(spinFrames[m.frame%len(spinFrames)]))
	}
	meta = append(meta, m.be.Cloud())
	if m.loaded {
		running, parked, hourly := 0, 0, 0.0
		for _, s := range m.sessions {
			switch s.State {
			case pier.StateRunning, pier.StateWorking, pier.StateIdle, pier.StateCreating:
				running++
				hourly += pier.HourlyUSD(s)
			case pier.StateParked:
				parked++
			}
		}
		meta = append(meta, fmt.Sprintf("%d running", running))
		if parked > 0 {
			meta = append(meta, fmt.Sprintf("%d parked", parked))
		}
		if len(m.ready) > 0 {
			meta = append(meta, fmt.Sprintf("%d ready", len(m.ready)))
		}
		if hourly > 0 {
			meta = append(meta, fmt.Sprintf("~$%.2f/h", hourly))
		}
	}
	if m.quota != "" {
		meta = append(meta, m.quota)
	}
	// Narrow windows drop the least important facts first (the list ends
	// with them) rather than losing the whole right side.
	for len(meta) > 0 {
		right := sDim.Render(strings.Join(meta, " · ")) + " "
		if gap := m.width - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			return left + strings.Repeat(" ", gap) + right
		}
		meta = meta[:len(meta)-1]
	}
	return fit(left, m.width)
}

func (m model) reminderCount() int {
	n := 0
	for _, r := range m.repos {
		n += len(r.Reminders)
	}
	return n
}

// split lays a list and a detail panel side by side on wide screens and
// stacked on narrow ones.
func (m model) split(list func(w, h int) string, detail func(w, h int) string) string {
	h := m.bodyHeight()
	if m.width >= 110 {
		lw := m.width * 58 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, list(lw, h), detail(m.width-lw, h))
	}
	dh := min(10, h/2)
	return list(m.width, h-dh) + "\n" + detail(m.width, dh)
}

// --- sessions ---------------------------------------------------------------------

func (m model) sessionsView() string {
	return m.split(m.sessionList, m.sessionDetail)
}

func (m model) sessionList(w, h int) string {
	inner := w - 4
	note := ""
	if m.loaded {
		note = strconv.Itoa(len(m.sessions))
	}
	var lines []string
	switch {
	case m.authRequired:
		lines = []string{"", sWarn.Render("your cloud login expired"), sDim.Render("press enter to sign in again")}
	case !m.loaded:
		lines = []string{"", sDim.Render("fetching sessions…")}
	case len(m.sessions) == 0:
		lines = emptySessions(m.repo)
	default:
		nameW, repoW := 4, 4
		for _, s := range m.sessions {
			nameW = max(nameW, lipgloss.Width(s.Name))
			repoW = max(repoW, lipgloss.Width(s.Repo))
		}
		nameW, repoW = min(nameW, max(inner/3, 12)), min(repoW, max(inner/5, 8))
		lines = append(lines, sDim.Render(fit("  NAME", nameW+2)+"  "+fit("REPO", repoW)+"  "+fit("AGE", 5)+"  STATE"))
		rows := h - 3
		start := scrollStart(m.sessIdx, len(m.sessions), rows)
		now := time.Now()
		for i := start; i < min(start+rows, len(m.sessions)); i++ {
			s := m.sessions[i]
			dot, word := badge(s, m.frame)
			row := dot + " " + fit(s.Name, nameW) + "  " + sFaint.Render(fit(s.Repo, repoW)) + "  " +
				sDim.Render(fit(pier.Age(s.Created, now), 5)) + "  " + word
			if i == m.sessIdx {
				row = selected(row, inner)
			}
			lines = append(lines, row)
		}
	}
	return panel("Sessions", note, lines, w, h, m.ov == ovNone)
}

func emptySessions(repo string) []string {
	lines := []string{"", sBold.Render("No sessions yet."), ""}
	if repo != "" {
		lines = append(lines,
			"Press "+sKey.Render("n")+" to start one from "+sAccent.Render(repo)+".",
			sDim.Render("It gets its own VM, your branch, secrets and agents,"),
			sDim.Render("and parks itself when you're away."))
	} else {
		lines = append(lines,
			"Run "+sAccent.Render("pier <branch>")+" inside a repo,",
			sDim.Render("or open pier from a repo and press n."))
	}
	return lines
}

// scrollStart keeps the cursor row visible in a window of rows.
func scrollStart(cursor, n, rows int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	start := cursor - rows/2
	return max(0, min(start, n-rows))
}

func (m model) sessionDetail(w, h int) string {
	s, ok := m.current()
	if !ok || m.authRequired {
		return panel("Details", "", nil, w, h, false)
	}
	_, word := badge(s, m.frame)
	lines := []string{field("state", word+stateNote(s))}
	if s.Branch != "" && s.Branch != s.Name {
		lines = append(lines, field("repo", s.Repo+sDim.Render(" · ")+s.Branch))
	} else {
		lines = append(lines, field("repo", s.Repo))
	}
	if s.State != pier.StateFailed {
		machine := s.InstanceType
		if s.CostNote != "" {
			machine += sDim.Render(" · " + s.CostNote)
		}
		lines = append(lines, field("machine", machine))
	}
	lines = append(lines, field("created", ago(s.Created)))
	switch s.Setup {
	case "running":
		lines = append(lines, field("setup", sAccent.Render("running")+sDim.Render(" — l follows it")))
	case "failed":
		lines = append(lines, field("setup", sBad.Render("failed")+sDim.Render(" — l shows why")))
	}
	lines = append(lines, "")
	for _, l := range wrap(sessionHint(s), w-4) {
		lines = append(lines, sDim.Render(l))
	}
	return panel(s.Name, "", lines, w, h, false)
}

func stateNote(s pier.Session) string {
	switch s.State {
	case pier.StateWorking:
		return sDim.Render(" · agent busy, you're detached")
	case pier.StateIdle:
		return sDim.Render(" · parks when the idle timer runs out")
	case pier.StateParked:
		return sDim.Render(" · disk only, nothing running")
	}
	return ""
}

func sessionHint(s pier.Session) string {
	switch s.State {
	case pier.StateParked:
		return "enter resumes it in about 20-60s, with your tmux windows and agent conversations back where you left them."
	case pier.StateCreating:
		return "Still setting up. The row flips when it's ready to attach."
	case pier.StateFailed:
		return "The create failed and rolled back — nothing is running or billing. " +
			tombstone.Summarize(s.FailReason) + " · l shows the create log, d clears the row."
	case pier.StateDeleting:
		return "Being deleted."
	}
	if s.Strained {
		return "Under sustained CPU or memory pressure — m resizes it in about a minute, disk intact."
	}
	return "enter attaches · detach with C-b d and it keeps running."
}

// --- repos ------------------------------------------------------------------------

func (m model) reposView() string {
	return m.split(m.repoList, m.repoDetail)
}

func (m model) repoList(w, h int) string {
	inner := w - 4
	var lines []string
	switch {
	case !m.loaded:
		lines = []string{"", sDim.Render("fetching…")}
	case len(m.repos) == 0:
		lines = []string{"", sBold.Render("No repos yet."), "", sDim.Render("Repos appear here once you start a session from one.")}
	default:
		nameW := 4
		for _, r := range m.repos {
			nameW = max(nameW, lipgloss.Width(r.Name))
		}
		nameW = min(nameW, max(inner/3, 12))
		lines = append(lines, sDim.Render(fit("  REPO", nameW+2)+"  "+fit("IMAGE", 18)+"  "+fit("READY", 7)+"  IDLE COST"))
		for i, r := range m.repos {
			mark := " "
			if len(r.Reminders) > 0 {
				mark = sWarn.Render("•")
			}
			if r.Current {
				mark = sAccent.Render("›")
				if len(r.Reminders) > 0 {
					mark = sWarn.Render("›")
				}
			}
			row := mark + " " + fit(r.Name, nameW) + "  " + fit(imageCell(r), 18) + "  " + fit(readyCell(r), 7) + "  " + sFaint.Render(money(r.MonthlyUSD))
			if i == m.repoIdx {
				row = selected(row, inner)
			}
			lines = append(lines, row)
		}
	}
	return panel("Repos", "› this repo", lines, w, h, m.ov == ovNone)
}

func imageCell(r pier.Repo) string {
	switch {
	case pier.BakeRunning(r.Name):
		return sAccent.Render("baking…")
	case r.Image == "":
		return sDim.Render("none")
	case r.Baked == nil:
		return "baked"
	}
	kind := "prebuilt"
	if !r.Baked.RepoIncluded {
		kind = "toolchains"
	}
	return kind + sDim.Render(" · "+pier.Age(r.Baked.BakedAt, time.Now()))
}

func readyCell(r pier.Repo) string {
	if r.ReadyTarget == 0 && r.Ready+r.Filling == 0 {
		return sDim.Render("off")
	}
	c := fmt.Sprintf("%d/%d", r.Ready, r.ReadyTarget)
	if r.Filling > 0 {
		c += sAccent.Render("+")
	}
	return c
}

func money(usd float64) string {
	if usd < 0.5 {
		return "$0"
	}
	return fmt.Sprintf("~$%.0f/mo", usd)
}

func (m model) repoDetail(w, h int) string {
	r, ok := m.currentRepo()
	if !ok {
		return panel("Details", "", nil, w, h, false)
	}
	iw := w - 4
	var lines []string
	switch {
	case pier.BakeRunning(r.Name):
		lines = append(lines, field("image", sAccent.Render("baking")+sDim.Render(" · log "+tilde(pier.BakeLogPath(r.Name)))))
	case r.Image == "":
		lines = append(lines, field("image", sDim.Render("none — sessions run the full setup")))
	case r.Baked == nil:
		lines = append(lines, field("image", r.Image))
	default:
		what := "toolchains + repo prebuilt"
		if !r.Baked.RepoIncluded {
			what = "toolchains only"
		}
		lines = append(lines, field("image", what+sDim.Render(" · baked "+ago(r.Baked.BakedAt))))
	}
	ready := fmt.Sprintf("keep %d", r.ReadyTarget)
	if r.ReadyTarget == 0 {
		ready = "off"
	}
	var parts []string
	if r.Ready > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", r.Ready))
	}
	if r.Filling > 0 {
		parts = append(parts, fmt.Sprintf("%d filling", r.Filling))
	}
	if r.Stale > 0 {
		parts = append(parts, fmt.Sprintf("%d recycling", r.Stale))
	}
	if len(parts) > 0 {
		ready += sDim.Render(" · " + strings.Join(parts, ", "))
	}
	lines = append(lines, field("ready", ready))
	lines = append(lines, field("sessions", strconv.Itoa(r.Sessions)))
	lines = append(lines, field("idle cost", money(r.MonthlyUSD)+sDim.Render(" while nothing runs")))
	lines = append(lines, "")
	for _, rem := range r.Reminders {
		for i, l := range wrap(rem.Message, iw-2) {
			prefix := "  "
			if i == 0 {
				prefix = sWarn.Render("! ")
			}
			lines = append(lines, prefix+l)
		}
	}
	if len(r.Reminders) > 0 {
		lines = append(lines, "")
	}
	var help string
	if r.Current {
		help = "b bakes the image (background). +/- sets ready sessions: parked, set-up copies that make the next session start in ~25s, each " +
			money(m.be.DiskMonthlyUSD()) + " while waiting. x removes them."
	} else {
		help = "Bake and refill from inside this repo. x removes its ready sessions from anywhere."
	}
	for _, l := range wrap(help, iw) {
		lines = append(lines, sDim.Render(l))
	}
	return panel(r.Name, "", lines, w, h, false)
}

// --- settings ---------------------------------------------------------------------

func (m model) settingsView() string {
	h := m.bodyHeight()
	detailH := min(8, h/3)
	listH := h - detailH
	inner := m.width - 4

	labelW, valW := 0, 0
	for _, f := range m.settings {
		labelW = max(labelW, lipgloss.Width(f.Label))
		valW = max(valW, lipgloss.Width(m.displayOf(f)))
	}
	valW = min(valW, 28)

	var lines []string
	cursorLine := 0
	for _, g := range config.Groups {
		first := true
		for i, f := range m.settings {
			if f.Group != g.Key {
				continue
			}
			if first {
				if len(lines) > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, sBrand.Render(g.Title))
				first = false
			}
			if i == m.setIdx {
				cursorLine = len(lines)
			}
			row := "  " + fit(f.Label, labelW) + "   " + fit(m.displayOf(f), valW) + "   " + sDim.Render(f.Hint)
			if i == m.setIdx {
				row = selected(row, inner)
			}
			lines = append(lines, row)
		}
	}
	rows := listH - 2
	if len(lines) > rows {
		start := scrollStart(cursorLine, len(lines), rows)
		lines = lines[start:]
	}
	list := panel("Settings", "apply to new sessions", lines, m.width, listH, m.ov == ovNone)

	var about []string
	if len(m.settings) > 0 {
		f := m.settings[m.setIdx]
		for _, l := range wrap(f.Detail, inner) {
			about = append(about, sFaint.Render(l))
		}
		if f.Default != "" {
			about = append(about, sDim.Render("default: "+f.Default))
		}
	}
	title := "About"
	if len(m.settings) > 0 {
		title = m.settings[m.setIdx].Label
	}
	return list + "\n" + panel(title, "", about, m.width, detailH, false)
}

// displayOf renders a setting's value; machine rows add their specs.
func (m model) displayOf(f pier.SettingValue) string {
	if f.Key == "speed.profile" && f.Value == "custom" {
		return "Custom"
	}
	return f.Display
}

// --- overlays ---------------------------------------------------------------------

// sheet is the bottom sheet for quick prompts: new session, confirm, edit.
func (m model) sheet() string {
	var title string
	var line string
	switch m.ov {
	case ovNew:
		title = "New session from " + m.repo
		line = sAccent.Render("branch ❯ ") + m.input + sAccent.Render("▌")
	case ovConfirm:
		title = "Confirm"
		line = sWarn.Render(m.confirmQ) + "  " + keys("y", "yes", "n", "no")
	case ovEdit:
		f := m.settings[m.setIdx]
		title = f.Label
		line = sAccent.Render("❯ ") + m.input + sAccent.Render("▌")
	default:
		return ""
	}
	return panel(title, "", []string{line}, m.width, 3, true) + "\n"
}

func (m model) footer() string {
	status := ""
	if m.status != "" {
		if m.statusBad {
			status = " " + sBad.Render("! "+m.status)
		} else {
			status = " " + sAccent.Render("▸ ") + m.status
		}
	}
	var k []string
	switch {
	case m.ov == ovNew || m.ov == ovEdit:
		k = []string{"enter", "save", "esc", "cancel"}
	case m.ov == ovConfirm:
	case m.ov == ovLogs:
		k = []string{"↑↓", "scroll", "space", "page", "G", "follow", "esc", "back"}
	case m.ov == ovResize || m.ov == ovPick:
		k = []string{"↑↓", "choose", "enter", "select", "esc", "cancel"}
	case m.ov == ovHelp:
		k = []string{"any key", "close"}
	case m.authRequired:
		k = []string{"enter", "sign in", "q", "quit"}
	case m.tab == tabSessions:
		k = []string{"enter", "attach", "n", "new", "l", "logs", "m", "resize", "p", "keep", "d", "delete", "tab", "repos"}
	case m.tab == tabRepos:
		k = []string{"b", "bake", "+/-", "ready", "x", "remove ready", "tab", "settings"}
	default:
		k = []string{"enter", "change", "↑↓", "move", "tab", "sessions"}
	}
	if m.ov == ovNone && !m.authRequired {
		k = append(k, "?", "help", "q", "quit")
	}
	return fit(status, m.width) + "\n" + fitKeys(k, m.width-1)
}

// fitKeys renders as many hint pairs as fit in w, always keeping the last
// two (help and quit) so a narrow window still says how to get out.
func fitKeys(pairs []string, w int) string {
	if len(pairs) == 0 {
		return ""
	}
	tail := pairs[max(len(pairs)-4, 0):]
	head := pairs[:max(len(pairs)-4, 0)]
	for {
		all := append(append([]string{}, head...), tail...)
		if line := " " + keys(all...); lipgloss.Width(line) <= w || len(head) == 0 {
			return fit(line, w+1)
		}
		head = head[:len(head)-2]
	}
}

// centered draws a picker panel in the middle of the body area.
func (m model) centered(title, note string, lines []string) string {
	w := min(max(m.width*2/3, 50), m.width)
	h := min(len(lines)+2, m.bodyHeight())
	box := panel(title, note, lines, w, h, true)
	return lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, box)
}

func (m model) resizeView() string {
	s, _ := m.current()
	var lines []string
	for i, mc := range m.machines {
		row := fmt.Sprintf("%-14s %3s vCPU  %4s GB  %s", mc.Type, mc.CPU, mc.Mem, mc.Cost)
		if mc.Type == s.InstanceType {
			row += sDim.Render("  current")
		}
		if i == m.pickIdx {
			row = selected(row, min(max(m.width*2/3, 50), m.width)-4)
		}
		lines = append(lines, row)
	}
	return m.centered("Resize "+s.Name, "same CPU arch · on-demand while running", lines)
}

func (m model) pickView() string {
	f := m.settings[m.setIdx]
	w := min(max(m.width*2/3, 50), m.width) - 4
	labelW := 0
	for _, o := range m.pickOpts {
		l := o.Label
		if l == "" {
			l = o.Value
		}
		labelW = max(labelW, lipgloss.Width(l))
	}
	var lines []string
	for i, o := range m.pickOpts {
		l := o.Label
		if l == "" {
			l = o.Value
		}
		row := fit(l, labelW) + "   " + sDim.Render(o.Desc)
		if o.Value == f.Value {
			row = fit(l, labelW) + "   " + sDim.Render(o.Desc) + sAccent.Render("  ✓")
		}
		if i == m.pickIdx {
			row = selected(row, w)
		}
		lines = append(lines, row)
	}
	return m.centered(f.Label, f.Hint, lines)
}

func (m model) helpView() string {
	sec := func(t string) string { return sBrand.Render(t) }
	row := func(k, d string) string { return "  " + sKey.Render(fit(k, 10)) + d }
	lines := []string{
		sec("Everywhere"),
		row("tab ← →", "switch tabs (or 1 2 3)"),
		row("r", "refresh"),
		row("s", "settings"),
		row("q", "quit"),
		"",
		sec("Sessions"),
		row("enter", "attach — a parked session resumes with tmux restored"),
		row("n", "new session from this repo (runs in the background)"),
		row("l", "setup log, live"),
		row("m", "resize the VM"),
		row("p", "keep: never park when idle"),
		row("d", "destroy (or clear a failed create)"),
		"",
		sec("Repos"),
		row("b", "bake this repo's session image"),
		row("+ -", "ready sessions: parked, set-up copies for ~25s starts"),
		row("x", "remove a repo's ready sessions"),
		"",
		sDim.Render("  Sessions park themselves when you're away: only the disk costs money."),
		sDim.Render("  The CLI does all of this too — `pier help`."),
	}
	return m.centered("Help", "pier "+m.opts.Version, lines)
}

func itoa(n int) string { return strconv.Itoa(n) }

// Package tui is the bare `pier` screen: a session list you can attach to,
// plus new/delete/pin/refresh. Full-screen on the alternate buffer, one
// accent color, states color-coded; quitting restores the shell untouched.
// Attach hands the terminal to ssh via tea.ExecProcess, so a tmux detach
// lands back on this list instead of the shell. Quota loads asynchronously
// so the screen opens instantly; a `loaded` flag separates "fetching" from
// "genuinely empty" so the list never flashes "0 sessions" before the first
// fetch lands.
package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kerem-kaynak/pier/internal/config"
	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/pool"
	"github.com/kerem-kaynak/pier/internal/ui"
)

type Options struct {
	FetchQuota func() string // e.g. "12/32 vCPUs in use"; nil/"" hides it
	Fetch      func() ([]driver.Session, error)
	Destroy    func(driver.Session) error
	Pin        func(driver.Session) error
	// CreateDetached starts a background create and returns its log path;
	// the TUI stays open and the session appears in the list as "creating".
	// nil disables creating from the TUI (tests).
	CreateDetached func(branch string) (logPath string, err error)
	// Resize + Machines power the m key: Machines lists same-arch picks for a
	// session so nobody memorizes type names. nil hides the key.
	Resize   func(s driver.Session, instanceType string) error
	Machines func(s driver.Session) []driver.Machine
	// SettingsMachines is the machine catalog for the settings machine picker,
	// given a driver id ("aws-ec2"/"gcp-gce") and the currently configured
	// type. nil falls the machine fields back to free-text editing.
	SettingsMachines func(driverID, currentType string) []driver.Machine
	// FetchLog returns a session's setup log for the l key's in-place viewer.
	// nil hides the key.
	FetchLog func(s driver.Session) (string, error)
	// Attach returns a fresh ssh command for one attach attempt; the TUI runs
	// it via tea.ExecProcess so the program survives a detach and lands back
	// on the list. nil disables the enter key (tests).
	Attach func(s driver.Session) (*exec.Cmd, error)
	// Resume unparks a session's VM and blocks until its transport answers.
	Resume func(s driver.Session) error
	// RetryAttach + WaitReachable power the one bounded reconnect after a fast
	// ssh transport failure (fresh or just-resumed VMs); nil disables the retry.
	RetryAttach   func(err error, elapsed time.Duration) bool
	WaitReachable func(s driver.Session) error
	// Pools power the w key's warm-pool page and the header's warm count.
	// PoolSizes re-reads the configured repo→size map (config may change
	// between opens). CurrentRepo/CurrentGen describe the repo pier launched
	// from ("" outside a repo): only its row is size-editable — a fill needs
	// the local checkout — and only its members can be staleness-checked (the
	// generation hashes the local setup script). nil PoolSizes hides the page.
	PoolSizes   func() map[string]int
	CurrentRepo string
	CurrentGen  string
	// PoolMaxAge is the configured member recycle age (0 = don't check);
	// members past it show stale. PoolNote explains a pool page that is
	// read-only for a reason worth a sentence (a broken [pool] config, say)
	// instead of the generic run-from-a-repo hint.
	PoolMaxAge time.Duration
	PoolNote   string
	// PoolSet saves the current repo's size and reconciles: 0 drains inline,
	// >0 starts a detached fill and returns its log path.
	PoolSet func(size int) (logPath string, err error)
	// PoolFill starts a detached fill for the current repo.
	PoolFill func() (logPath string, err error)
	// PoolDrain destroys a repo's warm members — any repo; draining needs no
	// local checkout — and returns how many went down.
	PoolDrain func(repo string) (int, error)
}

// PoolStats is one repo's warm-member census, shared by the TUI page and
// `pier pool`.
type PoolStats struct {
	Ready, Filling, Stale int
	Ages                  []string // ready members' ages, newest first as listed
}

// FoldPool folds one repo's unclaimed members out of a session list, judging
// them the way reconcile would: dead, wrong-generation, over the recycle age,
// or unparked past the fill grace all count stale — a member whose fill died
// hours ago must not read "filling" forever. curGen gates the generation
// check: only the repo the process runs from can compute its current
// generation, so other repos' members never show gen-stale (they recycle on
// their own machines' claims). maxAge 0 skips the age check.
func FoldPool(sessions []driver.Session, repo, curRepo, curGen string, maxAge time.Duration, now time.Time) PoolStats {
	var st PoolStats
	for _, s := range sessions {
		if s.PoolGen == "" || s.Repo != repo {
			continue
		}
		lived := now.Sub(s.Created)
		switch {
		case s.State == driver.StateDeleting:
		case s.State == driver.StateDead,
			repo == curRepo && s.PoolGen != curGen,
			maxAge > 0 && lived >= maxAge:
			st.Stale++
		case s.State == driver.StateParked:
			st.Ready++
			st.Ages = append(st.Ages, age(s.Created))
		case lived >= pool.FillGrace:
			st.Stale++
		default:
			st.Filling++
		}
	}
	return st
}

func Run(opts Options) error {
	_, err := tea.NewProgram(model{opts: opts, loading: true}, tea.WithAltScreen()).Run()
	return err
}

type mode int

const (
	modeList mode = iota
	modeNew
	modeConfirm
	modeSettings
	modeResize
	modeLogs
	modePool
)

type model struct {
	opts      Options
	sessions  []driver.Session
	members   []driver.Session // unclaimed warm pool members, split from sessions
	quota     string
	cursor    int
	mode      mode
	input     string // branch name being typed in modeNew
	status    string // transient line: errors, confirmations
	statusBad bool   // status is an error (red) vs a notice (accent)
	loading   bool
	loaded    bool // first fetch has landed
	polling   bool // an auto-refresh poll is scheduled (creates in flight)
	watch     int  // poll rounds left after a spawn (until it's listable)
	frame     int  // spinner frame
	// attach-in-flight state; enter is a no-op while attachSess is set
	attachSess    driver.Session // row being attached; zero Name = none in flight
	attachStart   time.Time      // last ExecProcess launch; feeds RetryAttach
	attachRetried bool           // one bounded reconnect per attach, like the CLI
	// settings page state; cfg loads fresh each time s opens the page
	cfg      *config.Config
	setIdx   int  // index into config.Settings (the selected field)
	editing  bool // free-text editor open for the selected field
	setInput string
	// settings picker sub-state: a choice/machine field's dropdown
	picking  bool
	pickOpts []config.Option // options shown (machine catalog converted in)
	pickIdx  int
	// resize picker state; machines reload each time m opens the picker
	machines []driver.Machine
	machIdx  int
	// pool page state (modePool); sizes reload each time w opens the page
	poolRepos   []string // rows: configured repos ∪ repos with members ∪ current repo
	poolSizes   map[string]int
	poolIdx     int
	poolPend    int  // pending size for the current repo's row; -1 = none
	poolConfirm bool // drain y/n pending
	// log viewer state (modeLogs); content refetches on a slow cadence so a
	// still-running setup streams in place
	logSess    driver.Session
	logText    string   // sanitized log, source of truth for rewrapping
	logLines   []string // logText wrapped to the terminal width
	logOff     int      // first visible wrapped line
	logStick   bool     // pinned to the tail: new content keeps the end in view
	logLoading bool
	logPolling bool
	width      int
	height     int
}

func anyCreating(sessions []driver.Session) bool {
	for _, s := range sessions {
		if s.State == driver.StateCreating {
			return true
		}
	}
	return false
}

// anyFilling reports whether an unclaimed member is mid-fill (not yet parked);
// the open pool page keeps refreshing while one is.
func anyFilling(members []driver.Session) bool {
	for _, s := range members {
		switch s.State {
		case driver.StateParked, driver.StateDead, driver.StateDeleting:
		default:
			return true
		}
	}
	return false
}

// splitMembers separates unclaimed pool members from real sessions: members
// never sit in the session table (they're plumbing, not work) — they show on
// the pool page and in the header's warm count.
func splitMembers(all []driver.Session) (sessions, members []driver.Session) {
	for _, s := range all {
		if s.PoolGen != "" {
			members = append(members, s)
		} else {
			sessions = append(sessions, s)
		}
	}
	return sessions, members
}

type sessionsMsg struct {
	sessions []driver.Session
	err      error
}

type quotaMsg string

type tickMsg struct{}

// pollMsg drives the slow auto-refresh that runs while a session is creating,
// so a background create's progress lands in the list without pressing r.
type pollMsg struct{}

// spawnedMsg reports a detached create kicked off (or failed to).
type spawnedMsg struct {
	branch string
	log    string
	err    error
}

// poolActMsg reports an async pool action (size apply, fill, drain).
type poolActMsg struct {
	note string
	err  error
}

// resumedMsg reports a parked VM's blocking resume finished (or failed).
type resumedMsg struct {
	s   driver.Session
	err error
}

// attachDoneMsg arrives when the foreground ssh exits: detach, remote exit,
// or transport error.
type attachDoneMsg struct {
	s   driver.Session
	err error
}

// reachableMsg reports the bounded wait before the one attach retry.
type reachableMsg struct {
	s   driver.Session
	err error
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func pollCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m model) fetch() tea.Msg {
	ss, err := m.opts.Fetch()
	return sessionsMsg{ss, err}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetch, tickCmd()}
	if m.opts.FetchQuota != nil {
		cmds = append(cmds, func() tea.Msg { return quotaMsg(m.opts.FetchQuota()) })
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionsMsg:
		m.loading = false
		m.loaded = true
		if msg.err != nil {
			m.status, m.statusBad = msg.err.Error(), true
			return m, nil
		}
		m.sessions, m.members = splitMembers(msg.sessions)
		if m.cursor >= len(m.sessions) {
			m.cursor = max(0, len(m.sessions)-1)
		}
		if m.mode == modePool {
			m.reindexPools()
		}
		// While anything is still creating, keep refreshing on our own so a
		// background create walks to "running" without the user pressing r —
		// likewise while the open pool page has a member mid-fill.
		if !m.polling && (anyCreating(m.sessions) || m.mode == modePool && anyFilling(m.members)) {
			m.polling = true
			return m, pollCmd()
		}
		return m, nil
	case pollMsg:
		m.polling = false
		if m.watch > 0 {
			m.watch--
		}
		// watch keeps the chain alive right after a spawn, before the new
		// instance is visible in the provider's list.
		if m.watch > 0 || anyCreating(m.sessions) || m.mode == modePool && anyFilling(m.members) {
			m.polling = true
			return m, tea.Batch(m.fetch, pollCmd()) // silent refresh: no spinner flicker
		}
		return m, nil
	case spawnedMsg:
		if msg.err != nil {
			m.status, m.statusBad = msg.err.Error(), true
			return m, nil
		}
		// Terse on purpose: the ◐ row appearing below already says "watch
		// here", and every extra word pushes the log path off the right edge
		// of a ~100-column terminal.
		m.status, m.statusBad = "creating "+msg.branch+" in the background (log: "+ui.Tilde(msg.log)+")", false
		m.watch = 12 // ~60s of polling even before the instance shows up
		if !m.polling {
			m.polling = true
			return m, pollCmd()
		}
		return m, nil
	case quotaMsg:
		m.quota = string(msg)
		return m, nil
	case poolActMsg:
		m.loading = false
		if msg.err != nil {
			m.status, m.statusBad = msg.err.Error(), true
			return m, nil
		}
		m.status, m.statusBad = msg.note, false
		if m.opts.PoolSizes != nil {
			m.poolSizes = m.opts.PoolSizes() // a size apply just changed config
		}
		m.reindexPools()
		m.loading = true
		// A detached fill takes a beat to launch its instance; without the
		// watch window the poll chain would die on the first refresh that
		// still shows nothing filling — same grace a spawned create gets.
		m.watch = max(m.watch, 24) // ~2min
		cmds := []tea.Cmd{m.fetch, tickCmd()}
		if !m.polling { // a fill may now be in flight — watch it land
			m.polling = true
			cmds = append(cmds, pollCmd())
		}
		return m, tea.Batch(cmds...)
	case resumedMsg:
		m.loading = false
		if msg.err != nil {
			m.attachSess = driver.Session{}
			m.status, m.statusBad = "resume "+msg.s.Name+": "+msg.err.Error(), true
			return m, nil
		}
		return m.startAttach(msg.s)
	case attachDoneMsg:
		if msg.err == nil { // clean detach or remote exit
			m.attachSess, m.attachRetried = driver.Session{}, false
			m.loading = true
			m.status, m.statusBad = "detached from "+msg.s.Name+" — it keeps running", false
			return m, tea.Batch(m.fetch, tickCmd())
		}
		if !m.attachRetried && m.opts.RetryAttach != nil && m.opts.WaitReachable != nil &&
			m.opts.RetryAttach(msg.err, time.Since(m.attachStart)) {
			m.attachRetried = true
			m.loading = true
			m.status, m.statusBad = "not reachable yet — waiting for "+msg.s.Name+" to come online (~30-60s)", false
			wait := m.opts.WaitReachable
			return m, tea.Batch(tickCmd(), func() tea.Msg { return reachableMsg{msg.s, wait(msg.s)} })
		}
		status := "attach " + msg.s.Name + ": " + msg.err.Error()
		var exitErr *exec.ExitError
		if errors.As(msg.err, &exitErr) && exitErr.ExitCode() != 255 && time.Since(m.attachStart) < 15*time.Second {
			// the remote bootstrap-marker guard exits 1 fast when setup is
			// still running — point at the log instead of a bare exit status
			status += " — it may still be setting up; l shows the setup log"
		}
		m.attachSess, m.attachRetried = driver.Session{}, false
		m.loading = true
		m.status, m.statusBad = status, true
		return m, tea.Batch(m.fetch, tickCmd()) // the refreshed state often explains it
	case reachableMsg:
		m.loading = false
		if msg.err != nil {
			m.attachSess, m.attachRetried = driver.Session{}, false
			m.status, m.statusBad = msg.err.Error(), true
			return m, nil
		}
		return m.startAttach(msg.s) // second and final attempt; attachRetried stays set
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeLogs {
			m.relayoutLog()
		}
		return m, nil
	case logMsg:
		if m.mode != modeLogs || msg.name != m.logSess.Name {
			return m, nil // stale fetch from a viewer already left
		}
		m.logLoading = false
		if msg.err != nil {
			m.status, m.statusBad = msg.err.Error(), true
			return m, nil
		}
		m.status = ""
		m.setLogText(msg.text)
		return m, nil
	case logPollMsg:
		m.logPolling = false
		if m.mode != modeLogs {
			return m, nil
		}
		m.logPolling = true
		cmds := []tea.Cmd{logPollCmd()}
		if !m.logLoading {
			m.logLoading = true
			cmds = append(cmds, m.fetchLog())
		}
		return m, tea.Batch(cmds...)
	case tickMsg:
		if m.loading {
			m.frame++
			return m, tickCmd()
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeNew:
			return m.updateNew(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modeSettings:
			return m.updateSettings(msg)
		case modeResize:
			return m.updateResize(msg)
		case modeLogs:
			return m.updateLogs(msg)
		case modePool:
			return m.updatePool(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.sessions)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.sessions) == 0 || m.opts.Attach == nil || m.attachSess.Name != "" {
			return m, nil
		}
		s := m.sessions[m.cursor]
		switch s.State {
		case driver.StateCreating:
			m.status, m.statusBad = s.Name+" is still setting up — attach when it shows running", false
			return m, nil
		case driver.StateDeleting:
			m.status, m.statusBad = s.Name+" is being deleted", false
			return m, nil
		case driver.StateDead:
			m.status, m.statusBad = s.Name+" is dead — no VM to attach to", false
			return m, nil
		case driver.StateParked:
			if m.opts.Resume == nil {
				m.status, m.statusBad = s.Name+" is parked — pier attach "+s.Name+" resumes it", false
				return m, nil
			}
			m.attachSess, m.loading = s, true
			m.status, m.statusBad = "resuming "+s.Name+" (~20-60s)…", false
			resume := m.opts.Resume
			return m, tea.Batch(tickCmd(), func() tea.Msg { return resumedMsg{s, resume(s)} })
		}
		return m.startAttach(s)
	case "l":
		if len(m.sessions) > 0 && m.opts.FetchLog != nil {
			s := m.sessions[m.cursor]
			switch s.State {
			case driver.StateCreating:
				m.status, m.statusBad = s.Name+" is still setting up — logs once it shows running", false
				return m, nil
			case driver.StateDeleting:
				m.status, m.statusBad = s.Name+" is being deleted", false
				return m, nil
			case driver.StateParked:
				// resuming a VM is a billing change — never a side effect of
				// opening a log view
				m.status, m.statusBad = s.Name+" is parked — pier logs "+s.Name+" resumes it and prints the log", false
				return m, nil
			case driver.StateDead:
				m.status, m.statusBad = s.Name+" is dead — no VM to read the log from", false
				return m, nil
			}
			m.mode = modeLogs
			m.logSess, m.logText, m.logLines = s, "", nil
			m.logOff, m.logStick, m.logLoading = 0, true, true
			cmds := []tea.Cmd{m.fetchLog()}
			if !m.logPolling {
				m.logPolling = true
				cmds = append(cmds, logPollCmd())
			}
			return m, tea.Batch(cmds...)
		}
	case "n":
		m.mode = modeNew
		m.input = ""
	case "d":
		if len(m.sessions) > 0 {
			m.mode = modeConfirm
		}
	case "p":
		if len(m.sessions) > 0 {
			s := m.sessions[m.cursor]
			m.loading = true
			return m, tea.Batch(func() tea.Msg {
				if err := m.opts.Pin(s); err != nil {
					return sessionsMsg{nil, err}
				}
				return m.fetch()
			}, tickCmd())
		}
	case "m":
		if len(m.sessions) > 0 && m.opts.Resize != nil && m.opts.Machines != nil {
			s := m.sessions[m.cursor]
			if s.State == driver.StateCreating {
				m.status, m.statusBad = s.Name+" is still setting up — resize once it shows running", false
				return m, nil
			}
			if s.State == driver.StateDeleting {
				m.status, m.statusBad = s.Name+" is being deleted", false
				return m, nil
			}
			m.machines = m.opts.Machines(s)
			if len(m.machines) == 0 {
				m.status, m.statusBad = "no machine picks for this driver — use pier resize <session> <type>", false
				return m, nil
			}
			m.machIdx = 0
			for i, mc := range m.machines {
				if mc.Type == s.InstanceType {
					m.machIdx = i
				}
			}
			m.mode = modeResize
		}
	case "w":
		if m.opts.PoolSizes == nil {
			return m, nil
		}
		return m.openPools()
	case "s":
		cfg, err := config.Load()
		if err != nil {
			m.status, m.statusBad = err.Error(), true
			return m, nil
		}
		m.cfg, m.setIdx, m.editing, m.picking = &cfg, 0, false, false
		m.mode = modeSettings
	case "r":
		m.loading = true
		return m, tea.Batch(m.fetch, tickCmd())
	}
	return m, nil
}

// startAttach hands the terminal to ssh via tea.ExecProcess: bubbletea leaves
// the alt screen, restores the shell termios, runs the command, then restores
// the TUI and delivers attachDoneMsg — a tmux detach lands back on this list.
func (m model) startAttach(s driver.Session) (tea.Model, tea.Cmd) {
	cmd, err := m.opts.Attach(s)
	if err != nil {
		m.attachSess, m.loading = driver.Session{}, false
		m.status, m.statusBad = err.Error(), true
		return m, nil
	}
	m.attachSess, m.attachStart, m.loading = s, time.Now(), false
	m.status, m.statusBad = "attaching to "+s.Name+" — detach with C-b d (session keeps running)", false
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return attachDoneMsg{s, err} })
}

func (m model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		return m.updateSettingEdit(msg)
	}
	if m.picking {
		return m.updateSettingPick(msg)
	}
	m.status = ""
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.mode = modeList
	case "up", "k":
		if m.setIdx > 0 {
			m.setIdx--
		}
	case "down", "j":
		if m.setIdx < len(config.Settings)-1 {
			m.setIdx++
		}
	case "enter":
		return m.openSetting()
	}
	return m, nil
}

// openSetting opens the selected field for editing: a picker for choice and
// machine fields, the free-text editor for the rest. A machine field with no
// catalog wired (tests) falls back to the editor.
func (m model) openSetting() (tea.Model, tea.Cmd) {
	f := config.Settings[m.setIdx]
	cur := config.Get(*m.cfg, f.Key)
	switch f.Kind {
	case config.KindChoice:
		m.picking, m.pickOpts, m.pickIdx = true, f.Options, optIndex(f.Options, cur)
	case config.KindMachine:
		var cat []driver.Machine
		if m.opts.SettingsMachines != nil {
			cat = m.opts.SettingsMachines(driverID(f.Group), cur)
		}
		if len(cat) == 0 {
			m.editing, m.setInput = true, cur
			return m, nil
		}
		m.pickOpts = make([]config.Option, len(cat))
		for i, mc := range cat {
			m.pickOpts[i] = config.Option{Value: mc.Type, Desc: fmt.Sprintf("%2s vCPU · %3s GiB · %s", mc.CPU, mc.Mem, mc.Cost)}
		}
		m.picking, m.pickIdx = true, optIndex(m.pickOpts, cur)
	default:
		m.editing, m.setInput = true, cur
	}
	return m, nil
}

func (m model) updateSettingEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.editing, m.status = false, ""
	case "enter":
		return m.saveSetting(m.setInput)
	case "backspace":
		if len(m.setInput) > 0 {
			m.setInput = m.setInput[:len(m.setInput)-1]
		}
	default:
		if msg.Type == tea.KeyRunes && !strings.ContainsRune(string(msg.Runes), ' ') {
			m.setInput += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateSettingPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := config.Settings[m.setIdx]
	last := len(m.pickOpts) // the "custom…" row sits one past the real options
	if f.NoCustom {
		last = len(m.pickOpts) - 1
	}
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.picking, m.status = false, ""
	case "up", "k":
		if m.pickIdx > 0 {
			m.pickIdx--
		}
	case "down", "j":
		if m.pickIdx < last {
			m.pickIdx++
		}
	case "enter":
		if !f.NoCustom && m.pickIdx == len(m.pickOpts) { // custom… → free text
			m.picking = false
			m.editing, m.setInput = true, config.Get(*m.cfg, f.Key)
			return m, nil
		}
		return m.saveSetting(m.pickOpts[m.pickIdx].Value)
	}
	return m, nil
}

// saveSetting validates val through config.Set (the single write path) and
// persists it. A rejected value keeps the editor/picker open so it can be
// fixed; the config on disk is never touched by a bad value.
func (m model) saveSetting(val string) (tea.Model, tea.Cmd) {
	f := config.Settings[m.setIdx]
	if err := config.Set(m.cfg, f.Key, val); err != nil {
		m.status, m.statusBad = err.Error(), true
		return m, nil
	}
	if err := m.cfg.Save(); err != nil {
		m.status, m.statusBad = err.Error(), true
		return m, nil
	}
	m.editing, m.picking = false, false
	m.status, m.statusBad = f.Label+" saved — applies to new sessions", false
	return m, nil
}

// optIndex is the position of val in opts, or 0 (a custom value not in the
// list simply lands the cursor on the first option).
func optIndex(opts []config.Option, val string) int {
	for i, o := range opts {
		if o.Value == val {
			return i
		}
	}
	return 0
}

// driverID maps a settings group to the driver id whose machine catalog and
// conventions it follows.
func driverID(group string) string {
	if group == "gcp" {
		return "gcp-gce"
	}
	return "aws-ec2"
}

func (m model) updateResize(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.mode = modeList
	case "up", "k":
		if m.machIdx > 0 {
			m.machIdx--
		}
	case "down", "j":
		if m.machIdx < len(m.machines)-1 {
			m.machIdx++
		}
	case "enter":
		s := m.sessions[m.cursor]
		t := m.machines[m.machIdx].Type
		m.mode = modeList
		if t == s.InstanceType {
			m.status, m.statusBad = s.Name+" is already a "+t, false
			return m, nil
		}
		m.loading = true
		m.status, m.statusBad = "resizing "+s.Name+" to "+t+" — a running session rides one park+resume (~40-60s)", false
		return m, tea.Batch(func() tea.Msg {
			if err := m.opts.Resize(s, t); err != nil {
				return sessionsMsg{nil, err}
			}
			return m.fetch()
		}, tickCmd())
	}
	return m, nil
}

func (m model) updateNew(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeList
	case "enter":
		if m.input == "" {
			return m, nil
		}
		if m.opts.CreateDetached == nil { // no creator wired (tests)
			m.mode = modeList
			return m, nil
		}
		for _, s := range m.sessions {
			if s.Name == m.input {
				m.mode = modeList
				m.status, m.statusBad = fmt.Sprintf("session %q already exists", m.input), true
				return m, nil
			}
		}
		branch := m.input
		m.mode, m.input = modeList, ""
		return m, func() tea.Msg {
			log, err := m.opts.CreateDetached(branch)
			return spawnedMsg{branch, log, err}
		}
	case "backspace":
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if msg.Type == tea.KeyRunes && !strings.ContainsRune(string(msg.Runes), ' ') {
			m.input += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		s := m.sessions[m.cursor]
		m.mode = modeList
		m.loading = true
		return m, tea.Batch(func() tea.Msg {
			if err := m.opts.Destroy(s); err != nil {
				return sessionsMsg{nil, err}
			}
			return m.fetch()
		}, tickCmd())
	default:
		m.mode = modeList
	}
	return m, nil
}

// openPools enters the w page: sizes reload fresh (config may have changed
// under us), rows rebuild from config ∪ live members, and the cursor lands on
// the repo pier launched from — the only size-editable row.
func (m model) openPools() (tea.Model, tea.Cmd) {
	m.poolSizes = m.opts.PoolSizes()
	m.poolRepos = poolRepoRows(m.poolSizes, m.members, m.opts.CurrentRepo)
	m.poolIdx, m.poolPend, m.poolConfirm = 0, -1, false
	for i, r := range m.poolRepos {
		if r == m.opts.CurrentRepo {
			m.poolIdx = i
		}
	}
	m.mode = modePool
	if !m.polling && anyFilling(m.members) {
		m.polling = true
		return m, pollCmd()
	}
	return m, nil
}

// reindexPools rebuilds the page's rows after a refresh or action, keeping the
// cursor on the same repo when it survives. A vanished row (drained from
// another terminal mid-refresh) takes its staged edit and confirm prompt with
// it — they referred to a repo that no longer has a row.
func (m *model) reindexPools() {
	cur := ""
	if m.poolIdx < len(m.poolRepos) {
		cur = m.poolRepos[m.poolIdx]
	}
	m.poolRepos = poolRepoRows(m.poolSizes, m.members, m.opts.CurrentRepo)
	m.poolIdx = 0
	found := false
	for i, r := range m.poolRepos {
		if r == cur {
			m.poolIdx, found = i, true
		}
	}
	if !found {
		m.poolPend, m.poolConfirm = -1, false
	}
}

// poolRepoRows is every repo worth a row: configured pools, repos that still
// have members on the provider (orphans included — they cost money), and the
// repo pier launched from, so + works before any pool exists.
func poolRepoRows(sizes map[string]int, members []driver.Session, curRepo string) []string {
	set := map[string]bool{}
	for r := range sizes {
		set[r] = true
	}
	for _, s := range members {
		set[s.Repo] = true
	}
	if curRepo != "" {
		set[curRepo] = true
	}
	rows := make([]string, 0, len(set))
	for r := range set {
		rows = append(rows, r)
	}
	sort.Strings(rows)
	return rows
}

func (m model) updatePool(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.poolConfirm {
		m.poolConfirm = false
		if msg.String() == "y" {
			repo := m.poolRepos[m.poolIdx]
			drain := m.opts.PoolDrain
			m.loading = true
			return m, tea.Batch(func() tea.Msg {
				n, err := drain(repo)
				if err != nil {
					return poolActMsg{err: err}
				}
				return poolActMsg{note: fmt.Sprintf("drained %d member(s) from %s", n, repo)}
			}, tickCmd())
		}
		return m, nil
	}
	m.status = ""
	row := ""
	if m.poolIdx < len(m.poolRepos) {
		row = m.poolRepos[m.poolIdx]
	}
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		if m.poolPend >= 0 { // first esc cancels the unapplied size
			m.poolPend = -1
			return m, nil
		}
		m.mode = modeList
	case "up", "k":
		if m.poolIdx > 0 {
			m.poolIdx--
			m.poolPend = -1
		}
	case "down", "j":
		if m.poolIdx < len(m.poolRepos)-1 {
			m.poolIdx++
			m.poolPend = -1
		}
	case "+", "=", "right":
		return m.bumpPool(row, 1)
	case "-", "_", "left":
		return m.bumpPool(row, -1)
	case "enter":
		if row == "" || row != m.opts.CurrentRepo || m.opts.PoolSet == nil ||
			m.poolPend < 0 || m.poolPend == m.poolSizes[row] {
			m.poolPend = -1
			return m, nil
		}
		size, set := m.poolPend, m.opts.PoolSet
		m.poolPend = -1
		m.loading = true
		return m, tea.Batch(func() tea.Msg {
			logPath, err := set(size)
			switch {
			case err != nil:
				return poolActMsg{err: err}
			case size == 0:
				return poolActMsg{note: "pool off — members drained"}
			default:
				return poolActMsg{note: fmt.Sprintf("pool size %d — filling in the background (log: %s)", size, ui.Tilde(logPath))}
			}
		}, tickCmd())
	case "f":
		if row == "" || m.opts.PoolFill == nil {
			return m, nil
		}
		if row != m.opts.CurrentRepo {
			m.status, m.statusBad = "run pier from inside "+row+" to fill its pool — the fill needs the local checkout", false
			return m, nil
		}
		if m.poolSizes[row] == 0 {
			m.status, m.statusBad = "pool size is 0 — press + then enter to warm one", false
			return m, nil
		}
		fill := m.opts.PoolFill
		m.loading = true
		return m, tea.Batch(func() tea.Msg {
			logPath, err := fill()
			if err != nil {
				return poolActMsg{err: err}
			}
			return poolActMsg{note: "filling in the background (log: " + ui.Tilde(logPath) + ")"}
		}, tickCmd())
	case "d":
		if row == "" || m.opts.PoolDrain == nil {
			return m, nil
		}
		st := FoldPool(m.members, row, m.opts.CurrentRepo, m.opts.CurrentGen, m.opts.PoolMaxAge, time.Now())
		if st.Ready+st.Filling+st.Stale == 0 {
			m.status, m.statusBad = "no warm members to drain for "+row, false
			return m, nil
		}
		m.poolConfirm = true
	case "r":
		m.loading = true
		return m, tea.Batch(m.fetch, tickCmd())
	}
	return m, nil
}

// bumpPool nudges the pending size on the current repo's row; other repos
// can't be resized from here — a fill needs their local checkout.
func (m model) bumpPool(row string, delta int) (tea.Model, tea.Cmd) {
	if row == "" {
		return m, nil
	}
	if row != m.opts.CurrentRepo {
		m.status, m.statusBad = "run pier from inside "+row+" to resize its pool", false
		return m, nil
	}
	if m.poolPend < 0 {
		m.poolPend = m.poolSizes[row]
	}
	m.poolPend = min(8, max(0, m.poolPend+delta))
	return m, nil
}

// poolView is the w page: one row per repo with its configured size, live
// member census, and the monthly cost of what's parked. Only the current
// repo's row is size-editable (the generation hashes the local setup script);
// any repo's members can be drained.
func (m model) poolView() string {
	var b strings.Builder
	head := " " + ui.Title.Render("⚓ pier pools") +
		ui.Dim.Render("  parked sessions ready to claim — pier <branch> grabs one")
	if m.loading {
		head += " " + ui.Accent.Render(spinner[m.frame%len(spinner)])
	}
	b.WriteString("\n" + head + "\n\n")

	if len(m.poolRepos) == 0 {
		b.WriteString(ui.Dim.Render("   no pools — run pier from inside a repo and press + to warm one") + "\n")
	} else {
		repoW := 4
		for _, r := range m.poolRepos {
			repoW = max(repoW, len(r))
		}
		b.WriteString(ui.Dim.Render(fmt.Sprintf("   %-*s  %-6s  %s", repoW, "REPO", "SIZE", "WARM")) + "\n")
		ready, filling := 0, 0
		now := time.Now()
		rows := make([]string, 0, len(m.poolRepos))
		for i, repo := range m.poolRepos {
			st := FoldPool(m.members, repo, m.opts.CurrentRepo, m.opts.CurrentGen, m.opts.PoolMaxAge, now)
			ready += st.Ready
			filling += st.Filling
			size := fmt.Sprintf("%-6d", m.poolSizes[repo])
			if i == m.poolIdx && m.poolPend >= 0 && m.poolPend != m.poolSizes[repo] {
				size = fmt.Sprintf("%-6s", fmt.Sprintf("%d→%d", m.poolSizes[repo], m.poolPend))
			}
			marker, name := "   ", fmt.Sprintf("%-*s", repoW, repo)
			if i == m.poolIdx {
				marker = " " + ui.Accent.Render("▸") + " "
				name = ui.Bold.Render(name)
			}
			cell := poolCell(repo, m.poolSizes[repo], st, m.opts.CurrentRepo)
			if repo == m.opts.CurrentRepo {
				cell += ui.Dim.Render("  · this repo")
			}
			rows = append(rows, fmt.Sprintf("%s%s  %s  %s", marker, name, size, cell))
		}
		// Chrome around the row window: title block (3), table header (1),
		// cost (2) and the footer below (up to 4 lines).
		for _, row := range m.windowBody(rows, m.poolIdx, 10) {
			b.WriteString(row + "\n")
		}
		// Parked is the only state that costs disk money alone; a filling
		// member is a running instance billing at full rate until it parks.
		if ready > 0 || filling > 0 {
			cost := fmt.Sprintf("cost: %d parked × ~$3-4/mo disk", ready)
			if filling > 0 {
				cost += fmt.Sprintf(" · %d filling at full instance rate until parked", filling)
			}
			b.WriteString("\n " + ui.Dim.Render(cost) + "\n")
		}
	}
	b.WriteString("\n")

	if m.poolConfirm {
		b.WriteString(" " + ui.Warn.Render(fmt.Sprintf("drain %s — destroy its warm members? (y/n)", m.poolRepos[m.poolIdx])) + "\n")
		return b.String()
	}
	if m.status != "" {
		if m.statusBad {
			b.WriteString(" " + ui.Bad.Render("! "+m.status) + "\n")
		} else {
			b.WriteString(" " + ui.Accent.Render("▸ "+m.status) + "\n")
		}
	}
	if m.opts.CurrentRepo == "" {
		hint := "read-only: run pier from inside a repo to size its pool"
		if m.opts.PoolNote != "" {
			hint = "read-only: " + m.opts.PoolNote
		}
		b.WriteString(" " + ui.Dim.Render(hint) + "\n")
	}
	b.WriteString(" " + ui.Keys("+/-", "size", "enter", "apply", "f", "fill", "d", "drain", "r", "refresh", "esc", "back") + "\n")
	return b.String()
}

// poolCell describes one repo's members: ready with ages, filling, stale —
// or why an empty row exists at all.
func poolCell(repo string, size int, st PoolStats, curRepo string) string {
	var parts []string
	if st.Ready > 0 {
		cell := fmt.Sprintf("● %d ready", st.Ready)
		if len(st.Ages) > 0 {
			cell += " (" + strings.Join(st.Ages, ", ") + ")"
		}
		parts = append(parts, ui.OK.Render(cell))
	}
	if st.Filling > 0 {
		parts = append(parts, ui.Warn.Render(fmt.Sprintf("◐ %d filling", st.Filling)))
	}
	if st.Stale > 0 {
		parts = append(parts, ui.Bad.Render(fmt.Sprintf("✗ %d stale", st.Stale)))
	}
	if len(parts) == 0 {
		switch {
		case size == 0:
			return ui.Dim.Render("(off)")
		case repo == curRepo:
			return ui.Dim.Render("empty — f fills")
		default:
			return ui.Dim.Render("empty")
		}
	}
	if size == 0 {
		parts = append(parts, ui.Dim.Render("orphaned — pool is off; d drains"))
	}
	return strings.Join(parts, "  ")
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m model) View() string {
	if m.mode == modeSettings {
		return m.settingsView()
	}
	if m.mode == modeLogs {
		return m.logsView()
	}
	if m.mode == modePool {
		return m.poolView()
	}
	var b strings.Builder

	head := " " + ui.Title.Render("⚓ pier")
	var meta []string
	if m.loaded {
		meta = append(meta, fmt.Sprintf("%d session(s)", len(m.sessions)))
		if n := len(m.members); n > 0 {
			meta = append(meta, fmt.Sprintf("%d warm", n))
		}
	}
	if m.quota != "" {
		meta = append(meta, m.quota)
	}
	if len(meta) > 0 {
		head += ui.Dim.Render("  " + strings.Join(meta, " · "))
	}
	if m.loading {
		head += " " + ui.Accent.Render(spinner[m.frame%len(spinner)])
	}
	b.WriteString("\n" + head + "\n\n")

	switch {
	case !m.loaded:
		b.WriteString(ui.Dim.Render("   fetching sessions…") + "\n")
	case len(m.sessions) == 0:
		b.WriteString(ui.Dim.Render("   no sessions — press n to start one") + "\n")
	default:
		b.WriteString(m.table())
	}
	b.WriteString("\n")

	switch m.mode {
	case modeNew:
		b.WriteString(" " + ui.Dim.Render("new session branch") + "\n")
		b.WriteString(ui.Box.Render(ui.Accent.Render("❯ ")+m.input+ui.Accent.Render("▌")) + "\n")
		b.WriteString(" " + ui.Keys("enter", "create", "esc", "cancel") + "\n")
	case modeConfirm:
		b.WriteString(" " + ui.Warn.Render(fmt.Sprintf("destroy %q and its disk? (y/n)", m.sessions[m.cursor].Name)) + "\n")
	case modeResize:
		s := m.sessions[m.cursor]
		b.WriteString(" " + ui.Dim.Render("resize "+s.Name+" — same-arch machines, billed on-demand while running") + "\n")
		for i, mc := range m.machines {
			marker := "   "
			row := fmt.Sprintf("%-12s  %2s vCPU  %3s GiB  %s", mc.Type, mc.CPU, mc.Mem, mc.Cost)
			if mc.Type == s.InstanceType {
				row += "  (current)"
			}
			if i == m.machIdx {
				marker = " " + ui.Accent.Render("▸") + " "
				row = ui.Bold.Render(row)
			}
			b.WriteString(marker + row + "\n")
		}
		b.WriteString(" " + ui.Keys("enter", "resize", "esc", "cancel") + "\n")
	default:
		if m.status != "" {
			if m.statusBad {
				b.WriteString(" " + ui.Bad.Render("! "+m.status) + "\n")
			} else {
				b.WriteString(" " + ui.Accent.Render("▸ "+m.status) + "\n")
			}
		}
		keys := []string{"enter", "attach", "n", "new", "d", "delete", "p", "pin", "m", "resize", "l", "logs"}
		if m.opts.PoolSizes != nil {
			keys = append(keys, "w", "pools")
		}
		keys = append(keys, "s", "settings", "r", "refresh", "q", "quit")
		b.WriteString(" " + ui.Keys(keys...) + "\n")
	}
	return b.String()
}

// settingsView is the s page: fields grouped session → aws → gcp, the
// inactive cloud dimmed but still editable, a detail footer for the selected
// field, and a read-only section for what pier setup and pier bake manage.
// Values save straight to config.toml and apply to new sessions.
func (m model) settingsView() string {
	if m.picking {
		return m.pickerView()
	}
	fields := config.Settings

	labelW, valW := 0, len("(default VPC)")
	disp := make([]string, len(fields))
	for i, f := range fields {
		disp[i] = f.Display(config.Get(*m.cfg, f.Key))
		labelW = max(labelW, len(f.Label))
		valW = max(valW, len(disp[i]))
	}

	header := []string{
		"",
		" " + ui.Title.Render("⚓ pier settings") +
			ui.Dim.Render("  "+ui.Tilde(config.Path())+" · changes apply to new sessions"),
		"",
	}

	var body []string
	cursorLine, lastGroup := 0, ""
	for i, f := range fields {
		if f.Group != lastGroup {
			if lastGroup != "" {
				body = append(body, "")
			}
			body = append(body, m.groupHeader(f.Group))
			lastGroup = f.Group
		}
		if i == m.setIdx {
			cursorLine = len(body)
		}
		body = append(body, m.settingRow(i, f, disp[i], labelW, valW))
	}
	body = append(body, "")
	body = append(body, m.readonlyLines()...)

	var footer []string
	footer = append(footer, m.detailFooter(fields[m.setIdx])...)
	if m.status != "" {
		if m.statusBad {
			footer = append(footer, " "+ui.Bad.Render("! "+m.status))
		} else {
			footer = append(footer, " "+ui.Accent.Render("▸ "+m.status))
		}
	}
	if m.editing {
		footer = append(footer, " "+ui.Keys("enter", "save", "esc", "cancel"))
	} else {
		verb := "edit"
		if k := fields[m.setIdx].Kind; k == config.KindChoice || k == config.KindMachine {
			verb = "choose"
		}
		footer = append(footer, " "+ui.Keys("↑↓", "move", "enter", verb, "esc", "back"))
	}

	body = m.windowBody(body, cursorLine, len(header)+len(footer))
	return strings.Join(header, "\n") + "\n" +
		strings.Join(body, "\n") + "\n" +
		strings.Join(footer, "\n") + "\n"
}

// groupHeader labels a group; a cloud group that isn't the active driver reads
// dim with a "used when…" note, matching its dimmed rows.
func (m model) groupHeader(group string) string {
	if group == "session" {
		return " " + ui.Bold.Render("session")
	}
	if m.inactiveGroup(group) {
		return " " + ui.Dim.Render(group+" — used when cloud is "+cloudName(group))
	}
	return " " + ui.Bold.Render(group)
}

// settingRow renders one field: accent cursor + bold when selected, the whole
// line dimmed for the inactive cloud, the machine fields annotated with the
// current type's specs. Padded as plain text before styling so the %-*s width
// math survives (ANSI escapes would blow the column alignment).
func (m model) settingRow(i int, f config.Field, disp string, labelW, valW int) string {
	label := fmt.Sprintf("%-*s", labelW, f.Label)
	hint := f.Hint
	if f.Kind == config.KindMachine {
		if h := m.machineHint(f); h != "" {
			hint = h
		}
	}
	if i == m.setIdx {
		val := fmt.Sprintf("%-*s", valW, disp)
		if m.editing {
			val = m.setInput + ui.Accent.Render("▌")
		}
		return " " + ui.Accent.Render("▸") + " " + ui.Bold.Render(label) + "  " + val + "  " + ui.Dim.Render(hint)
	}
	val := fmt.Sprintf("%-*s", valW, disp)
	if m.inactiveGroup(f.Group) {
		return "   " + ui.Dim.Render(label+"  "+val+"  "+hint)
	}
	return "   " + label + "  " + val + "  " + ui.Dim.Render(hint)
}

// readonlyLines is the "managed elsewhere" section: what travels into sessions
// and what's baked, shown so the answer to "what's carried?" doesn't require
// opening config.toml. Strictly read-only — the write paths stay pier setup
// and pier bake. The cursor never enters it.
func (m model) readonlyLines() []string {
	c := m.cfg

	secrets := ui.Dim.Render("(none)")
	if n := len(c.Secrets.Manifest); n > 0 {
		show, more := c.Secrets.Manifest, ""
		if n > 3 {
			show, more = show[:3], fmt.Sprintf(" +%d more", n-3)
		}
		secrets = strings.Join(show, " · ") + more
	}
	token := "not set"
	if c.Secrets.ClaudeOAuthToken != "" {
		token = "set"
	}
	imgs := c.AWS.BakedAMIs
	if c.Driver == "gcp-gce" {
		imgs = c.GCP.BakedImages
	}
	baked := ui.Dim.Render("(none)")
	if len(imgs) > 0 {
		pairs := make([]string, 0, len(imgs))
		for repo, img := range imgs {
			pairs = append(pairs, repo+" → "+shorten(img, 22))
		}
		sort.Strings(pairs)
		more := ""
		if len(pairs) > 2 {
			pairs, more = pairs[:2], fmt.Sprintf(" +%d more", len(pairs)-2)
		}
		baked = strings.Join(pairs, " · ") + more
	}

	// Pad by rune count, not bytes: "secrets → sessions" carries a multi-byte
	// arrow, and %-*s would misalign the value column against the ASCII rows.
	labelW := len([]rune("secrets → sessions"))
	row := func(label, val, mgr string) string {
		if pad := labelW - len([]rune(label)); pad > 0 {
			label += strings.Repeat(" ", pad)
		}
		return "   " + ui.Dim.Render(label+"  ") + val + ui.Dim.Render("  ("+mgr+")")
	}
	pools := ui.Dim.Render("(none)")
	if len(c.Pool.Sizes) > 0 {
		pairs := make([]string, 0, len(c.Pool.Sizes))
		for repo, n := range c.Pool.Sizes {
			pairs = append(pairs, fmt.Sprintf("%s × %d", repo, n))
		}
		sort.Strings(pairs)
		pools = strings.Join(pairs, " · ")
	}

	return []string{
		" " + ui.Dim.Render("managed elsewhere"),
		row("secrets → sessions", secrets, "pier setup"),
		row("claude token", token, "claude setup-token"),
		row("baked images", baked, "pier bake"),
		row("warm pools", pools, "w / pier pool"),
	}
}

// detailFooter is the explanation panel for the selected field: a rule, the
// label + prose, the underlying config key and default, another rule.
func (m model) detailFooter(f config.Field) []string {
	rule := ui.Dim.Render(strings.Repeat("─", m.ruleWidth()))
	out := []string{rule}
	lines := strings.Split(f.Detail, "\n")
	out = append(out, " "+ui.Bold.Render(f.Label)+ui.Dim.Render(" — "+lines[0]))
	for _, l := range lines[1:] {
		out = append(out, " "+ui.Dim.Render(l))
	}
	meta := "config: " + f.Key
	if f.Default != "" {
		meta += " · default: " + f.Default
	}
	return append(out, " "+ui.Dim.Render(meta), rule)
}

// pickerView is the dropdown for a choice or machine field: the options with
// their annotations, the current value marked, and (unless the field is a
// closed enum) a custom… row that drops into the free-text editor.
func (m model) pickerView() string {
	f := config.Settings[m.setIdx]
	var b strings.Builder
	b.WriteString("\n " + ui.Title.Render("⚓ pier settings") + ui.Dim.Render("  "+f.Label) + "\n\n")
	b.WriteString(" " + ui.Dim.Render(f.Label+" — "+strings.SplitN(f.Detail, "\n", 2)[0]) + "\n\n")

	cur := config.Get(*m.cfg, f.Key)
	leftW := 6
	labels := make([]string, len(m.pickOpts))
	for i, o := range m.pickOpts {
		labels[i] = o.Label
		if labels[i] == "" {
			labels[i] = o.Value
		}
		leftW = max(leftW, len(labels[i]))
	}
	for i, o := range m.pickOpts {
		row := fmt.Sprintf("%-*s", leftW, labels[i])
		if o.Desc != "" {
			row += "  " + o.Desc
		}
		if o.Value == cur {
			row += "  (current)"
		}
		b.WriteString(m.pickRow(i, row))
	}
	if !f.NoCustom {
		b.WriteString(m.pickRow(len(m.pickOpts), fmt.Sprintf("%-*s  ", leftW, "custom…")+"type your own"))
	}
	b.WriteString("\n " + ui.Keys("↑↓", "move", "enter", "select", "esc", "cancel") + "\n")
	return b.String()
}

func (m model) pickRow(i int, row string) string {
	if i == m.pickIdx {
		return " " + ui.Accent.Render("▸") + " " + ui.Bold.Render(row) + "\n"
	}
	return "   " + row + "\n"
}

// windowBody trims the settings body to what the terminal can hold, centered
// on the selected row — same idea as the session table, so the header and
// footer never scroll off the alternate screen. height 0 (tests) renders all.
func (m model) windowBody(body []string, cursorLine, chrome int) []string {
	if m.height == 0 {
		return body
	}
	budget := max(3, m.height-chrome-1)
	if len(body) <= budget {
		return body
	}
	lo := min(max(0, cursorLine-budget/2), len(body)-budget)
	return body[lo : lo+budget]
}

func (m model) inactiveGroup(group string) bool {
	if group != "aws" && group != "gcp" {
		return false
	}
	active := m.cfg.Driver
	if active == "" {
		active = "aws-ec2"
	}
	return driverID(group) != active
}

func (m model) machineHint(f config.Field) string {
	if m.opts.SettingsMachines == nil {
		return ""
	}
	cur := config.Get(*m.cfg, f.Key)
	for _, mc := range m.opts.SettingsMachines(driverID(f.Group), cur) {
		if mc.Type == cur {
			return fmt.Sprintf("%s vCPU · %s GiB · %s", mc.CPU, mc.Mem, mc.Cost)
		}
	}
	return ""
}

func (m model) ruleWidth() int {
	if m.width <= 0 {
		return 58
	}
	return min(58, max(20, m.width-2))
}

func cloudName(group string) string {
	if group == "gcp" {
		return "GCP"
	}
	return "AWS"
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// listRows is the table's row budget: terminal height minus every non-row
// line View emits in the current mode, so the footer never clips off the
// alternate screen (the renderer keeps the LAST height lines on overflow).
func (m model) listRows() int {
	if m.height == 0 { // no WindowSizeMsg yet (tests) — render everything
		return len(m.sessions)
	}
	chrome := 6 // top margin+title+blank, column header, blank after table, trailing newline
	switch m.mode {
	case modeNew:
		chrome += 5 // label + 3-line box + keys
	case modeConfirm:
		chrome++
	case modeResize:
		chrome += 2 + len(m.machines) // label + machine rows + keys
	default:
		chrome++ // keys
		if m.status != "" {
			chrome++
		}
	}
	return max(3, m.height-chrome)
}

// table renders the session rows: dim column header, accent cursor, states
// color-coded. Cells are padded as plain text first, then styled — ANSI
// escapes would defeat %-*s width math. When the terminal is shorter than
// the list, a cursor-centered window of rows renders instead.
func (m model) table() string {
	nameW, repoW, stateW := 4, 4, 5
	states := make([]string, len(m.sessions))
	for i, s := range m.sessions {
		states[i] = stateCell(s)
		nameW = max(nameW, len(s.Name))
		repoW = max(repoW, len(s.Repo))
		stateW = max(stateW, len([]rune(states[i])))
	}

	var b strings.Builder
	b.WriteString(ui.Dim.Render(fmt.Sprintf("   %-*s  %-*s  %-*s  %-4s  %s",
		nameW, "NAME", repoW, "REPO", stateW, "STATE", "AGE", "COST")) + "\n")
	rows, lo := m.listRows(), 0
	if rows < len(m.sessions) {
		lo = min(max(0, m.cursor-rows/2), len(m.sessions)-rows)
	}
	hi := min(len(m.sessions), lo+rows)
	for i := lo; i < hi; i++ {
		s := m.sessions[i]
		marker := "   "
		name := fmt.Sprintf("%-*s", nameW, s.Name)
		if i == m.cursor {
			marker = " " + ui.Accent.Render("▸") + " "
			name = ui.Bold.Render(name)
		}
		pad := stateW - len([]rune(states[i]))
		state := stateStyle(s).Render(states[i]) + strings.Repeat(" ", pad)
		fmt.Fprintf(&b, "%s%s  %s  %s  %-4s  %s\n",
			marker, name,
			ui.Dim.Render(fmt.Sprintf("%-*s", repoW, s.Repo)),
			state, age(s.Created), ui.Dim.Render(s.CostNote))
	}
	return b.String()
}

func stateCell(s driver.Session) string {
	dots := map[driver.State]string{
		driver.StateCreating: "◐",
		driver.StateRunning:  "●",
		driver.StateWorking:  "●",
		driver.StateIdle:     "●",
		driver.StateParked:   "◌",
		driver.StateDeleting: "◌",
		driver.StateDead:     "✗",
	}
	dot, ok := dots[s.State]
	if !ok {
		dot = "?"
	}
	cell := dot + " " + string(s.State)
	if s.Strained {
		cell += " ▲ strained"
	}
	switch s.Setup {
	case "running":
		cell += " (setting up)"
	case "failed":
		cell += " (setup failed)"
	}
	return cell
}

func stateStyle(s driver.Session) lipgloss.Style {
	if s.Setup == "failed" {
		return ui.Bad
	}
	if s.Strained {
		return ui.Strain
	}
	switch s.State {
	case driver.StateRunning:
		return ui.OK
	case driver.StateWorking:
		return ui.Accent
	case driver.StateCreating, driver.StateIdle:
		return ui.Warn
	case driver.StateDead:
		return ui.Bad
	default: // parked, deleting
		return ui.Dim
	}
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

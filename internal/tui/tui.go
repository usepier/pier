// Package tui is pier's app: Sessions, Repos and Settings tabs over the
// pkg/pier API. It holds no behavior of its own — every action is one call
// on the Backend — so the CLI, this TUI, and the Mac app stay in step.
package tui

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

// Backend is the slice of *pier.Client the app uses. An interface so tests
// can drive the app without a cloud.
type Backend interface {
	Cloud() string
	ListSessions(ctx context.Context) ([]pier.Session, error)
	Enrich(ctx context.Context, sessions []pier.Session) []pier.Session
	Repos(sessions []pier.Session, curRoot string) []pier.Repo
	Headroom(ctx context.Context) (pier.Quota, error)
	Remove(ctx context.Context, s pier.Session) error
	Keep(ctx context.Context, s pier.Session) error
	Resume(ctx context.Context, s pier.Session) error
	Resize(ctx context.Context, s pier.Session, machineType string) error
	Machines(s pier.Session) []pier.Machine
	Attach(ctx context.Context, s pier.Session) (*exec.Cmd, func(), error)
	WaitReachable(ctx context.Context, s pier.Session, timeout time.Duration) error
	SetupLog(ctx context.Context, s pier.Session, lines int) (string, error)
	SpawnCreate(repoRoot, branch string) (string, error)
	SpawnBake(repoRoot string) (string, error)
	SpawnFill(repoRoot string) (string, error)
	SetPoolSize(ctx context.Context, repoRoot string, n int, progress pier.Progress) (string, error)
	DrainPool(ctx context.Context, repo string, progress pier.Progress) (int, error)
	Settings() []pier.SettingValue
	Set(key, value string) error
	MachineCatalog(currentType string) []pier.Machine
	ReauthCommand() *exec.Cmd
	DiskMonthlyUSD() float64
}

// Options configures the app.
type Options struct {
	// Open builds a fresh backend: at start, and again after a settings
	// change that alters which cloud calls go to.
	Open     func() (Backend, error)
	RepoRoot string // the repo pier launched from; "" outside one
	Version  string
}

// ErrNoConfig means pier has never been set up on this machine.
var ErrNoConfig = errors.New("pier is not set up")

// Run opens the app full-screen until the user quits.
func Run(opts Options) error {
	be, err := opts.Open()
	if err != nil {
		if strings.Contains(err.Error(), "no config at") {
			return ErrNoConfig
		}
		return err
	}
	_, err = tea.NewProgram(newModel(opts, be), tea.WithAltScreen()).Run()
	return err
}

type tab int

const (
	tabSessions tab = iota
	tabRepos
	tabSettings
)

var tabNames = []string{"Sessions", "Repos", "Settings"}

// overlay is what sits on top of the current tab, if anything.
type overlay int

const (
	ovNone    overlay = iota
	ovNew             // branch name input
	ovConfirm         // y/n on confirmAction
	ovResize          // machine picker for the selected session
	ovLogs            // setup log viewer
	ovPick            // settings choice picker
	ovEdit            // settings free-text editor
	ovHelp
)

type model struct {
	opts Options
	be   Backend
	repo string // basename of opts.RepoRoot

	tab tab
	ov  overlay

	all      []pier.Session // everything Sessions() returned
	sessions []pier.Session // real sessions (ready ones split out)
	ready    []pier.Session
	repos    []pier.Repo
	quota    string

	loading, loaded bool
	authRequired    bool

	sessIdx, repoIdx, setIdx int

	input string // ovNew branch / ovEdit value

	// ovConfirm: the question and what a yes runs.
	confirmQ  string
	confirmDo func() tea.Cmd

	status    string
	statusBad bool
	statusAt  time.Time

	frame   int
	polling bool
	watch   int // extra poll rounds after a spawn, until the new row shows

	// attach in flight: one bounded reconnect per attach, like the CLI
	attaching     string
	attachStart   time.Time
	attachRetried bool

	machines []pier.Machine
	pickIdx  int
	pickOpts []config.Option

	settings []pier.SettingValue

	log logView

	width, height int
}

func newModel(opts Options, be Backend) model {
	m := model{opts: opts, be: be, loading: true, settings: be.Settings()}
	for _, f := range m.settings {
		if f.Key == "theme" {
			applyTheme(f.Value)
		}
	}
	if opts.RepoRoot != "" {
		m.repo = filepath.Base(opts.RepoRoot)
	}
	return m
}

// --- messages ---------------------------------------------------------------------

// sessionsMsg carries a list; enriched says whether the per-session status
// reads are in (the list shows first, statuses fill in after).
type sessionsMsg struct {
	all      []pier.Session
	err      error
	enriched bool
}
type quotaMsg string
type tickMsg struct{}
type pollMsg struct{}

// doneMsg reports a background action; refresh asks for a new list.
type doneMsg struct {
	note    string
	err     error
	refresh bool
	watch   bool // a new row is on its way: poll until it shows
}
type resumedMsg struct {
	s   pier.Session
	err error
}
type attachDoneMsg struct {
	s   pier.Session
	err error
}
type reachableMsg struct {
	s   pier.Session
	err error
}
type reauthMsg struct{ err error }
type reopenedMsg struct {
	be  Backend
	err error
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func pollCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m model) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	all, err := m.be.ListSessions(ctx)
	return sessionsMsg{all: all, err: err}
}

// enrich reads the running sessions' statuses for a list already on screen.
func (m model) enrich(all []pier.Session) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return sessionsMsg{all: be.Enrich(ctx, all), enriched: true}
	}
}

func (m model) fetchQuota() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q, err := m.be.Headroom(ctx)
	if err != nil {
		return quotaMsg("")
	}
	return quotaMsg(q.Detail)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetch, m.fetchQuota, tickCmd())
}

// act runs f in the background and reports its outcome as a doneMsg.
func act(f func() doneMsg) tea.Cmd { return func() tea.Msg { return f() } }

func (m *model) note(s string) { m.status, m.statusBad, m.statusAt = s, false, time.Now() }
func (m *model) fail(err error) {
	m.status, m.statusBad, m.statusAt = err.Error(), true, time.Now()
}

// inFlight reports whether something is changing on its own (a create, a
// refill, a bake), so the list keeps refreshing without a keypress.
func (m model) inFlight() bool {
	for _, s := range m.all {
		switch s.State {
		case pier.StateCreating, pier.StateDeleting:
			return true
		}
		if s.PoolGen != "" && s.State != pier.StateParked && s.State != pier.StateDead {
			return true
		}
	}
	for _, r := range m.repos {
		if pier.BakeRunning(r.Name) {
			return true
		}
	}
	return false
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.log.relayout(m.logWidth())
		return m, nil

	case tickMsg:
		m.frame++
		// Notices fade; errors stay until the next action.
		if m.status != "" && !m.statusBad && time.Since(m.statusAt) > 8*time.Second {
			m.status = ""
		}
		return m, tickCmd()

	case quotaMsg:
		m.quota = string(msg)
		return m, nil

	case sessionsMsg:
		m.loading = false
		if msg.err != nil {
			if pier.IsAuthExpired(msg.err) {
				m.authRequired = true
				m.status, m.statusBad = "your cloud login expired — enter signs in again", true
				return m, nil
			}
			m.fail(msg.err)
			return m, nil
		}
		m.loaded, m.authRequired = true, false
		if !msg.enriched {
			// Keep what the last status read said until the new one lands,
			// so rows don't blink between states on every refresh.
			msg.all = carryStatus(m.all, msg.all)
		}
		m.all = msg.all
		m.sessions, m.ready = pier.SplitPool(msg.all)
		// Stable order: the cloud returns instances in no particular order,
		// and rows that swap places between refreshes move the cursor off
		// what the user was looking at.
		sort.SliceStable(m.sessions, func(i, j int) bool { return m.sessions[i].Name < m.sessions[j].Name })
		m.repos = m.be.Repos(msg.all, m.opts.RepoRoot)
		m.sessIdx = clamp(m.sessIdx, len(m.sessions))
		m.repoIdx = clamp(m.repoIdx, len(m.repos))
		if m.watch > 0 {
			m.watch--
		}
		var cmds []tea.Cmd
		if !msg.enriched {
			cmds = append(cmds, m.enrich(msg.all))
		}
		if (m.inFlight() || m.watch > 0) && !m.polling {
			m.polling = true
			cmds = append(cmds, pollCmd())
		}
		return m, tea.Batch(cmds...)

	case pollMsg:
		// Background refreshes stay quiet: the spinner is for the first load
		// and for refreshes the user asked for.
		m.polling = false
		return m, m.fetch

	case doneMsg:
		if msg.err != nil {
			m.fail(msg.err)
		} else if msg.note != "" {
			m.note(msg.note)
		}
		if msg.watch {
			m.watch = 12
		}
		if msg.refresh {
			m.loading = true
			return m, m.fetch
		}
		return m, nil

	case resumedMsg:
		if msg.err != nil {
			m.attaching = ""
			m.fail(msg.err)
			return m, nil
		}
		msg.s.State = pier.StateRunning
		return m.execAttach(msg.s)

	case attachDoneMsg:
		if msg.err != nil && !m.attachRetried && pier.RetryAttach(msg.err, time.Since(m.attachStart)) {
			m.attachRetried = true
			m.note("not reachable yet — waiting for " + msg.s.Name + " to come online")
			s := msg.s
			return m, func() tea.Msg {
				return reachableMsg{s, m.be.WaitReachable(context.Background(), s, 4*time.Minute)}
			}
		}
		m.attaching = ""
		if msg.err != nil {
			m.fail(errors.New("attach: " + msg.err.Error()))
		}
		m.loading = true
		return m, m.fetch

	case reachableMsg:
		if msg.err != nil {
			m.attaching = ""
			m.fail(msg.err)
			return m, nil
		}
		return m.execAttach(msg.s)

	case reauthMsg:
		if msg.err != nil {
			m.fail(errors.New("sign-in didn't finish: " + msg.err.Error()))
			return m, nil
		}
		m.loading = true
		m.note("signed in — refreshing")
		return m, m.fetch

	case reopenedMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.be = msg.be
		m.settings = m.be.Settings()
		m.loading = true
		return m, tea.Batch(m.fetch, m.fetchQuota)

	case logMsg:
		return m.onLog(msg)

	case logPollMsg:
		if m.ov != ovLogs {
			m.log.polling = false
			return m, nil
		}
		return m, m.fetchLog()

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// carryStatus copies the previous status read onto a fresh list, for the
// sessions still in the same cloud state.
func carryStatus(prev, next []pier.Session) []pier.Session {
	old := map[string]pier.Session{}
	for _, s := range prev {
		old[s.ID] = s
	}
	for i, s := range next {
		p, ok := old[s.ID]
		if !ok || s.ID == "" || s.State != pier.StateRunning {
			continue
		}
		switch p.State {
		case pier.StateRunning, pier.StateWorking, pier.StateIdle:
			next[i].State, next[i].Attached = p.State, p.Attached
			next[i].Strained, next[i].Setup = p.Strained, p.Setup
		}
	}
	return next
}

func clamp(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// --- keys --------------------------------------------------------------------------

func (m model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.ov {
	case ovNew:
		return m.keyNew(k)
	case ovConfirm:
		return m.keyConfirm(k)
	case ovResize:
		return m.keyResize(k)
	case ovLogs:
		return m.keyLogs(k)
	case ovPick:
		return m.keyPick(k)
	case ovEdit:
		return m.keyEdit(k)
	case ovHelp:
		m.ov = ovNone
		return m, nil
	}

	if m.authRequired {
		switch k.String() {
		case "enter":
			return m, tea.ExecProcess(m.be.ReauthCommand(), func(err error) tea.Msg { return reauthMsg{err} })
		case "q", "esc":
			return m, tea.Quit
		}
		return m, nil
	}

	switch k.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.tab != tabSessions {
			m.tab = tabSessions
			return m, nil
		}
		return m, tea.Quit
	case "tab", "right":
		m.tab = (m.tab + 1) % 3
		return m, nil
	case "shift+tab", "left":
		m.tab = (m.tab + 2) % 3
		return m, nil
	case "1", "2", "3":
		m.tab = tab(k.String()[0] - '1')
		return m, nil
	case "?":
		m.ov = ovHelp
		return m, nil
	case "r":
		m.loading = true
		m.note("refreshing")
		return m, tea.Batch(m.fetch, m.fetchQuota)
	case "s":
		m.tab = tabSettings
		return m, nil
	}
	switch m.tab {
	case tabSessions:
		return m.keySessions(k)
	case tabRepos:
		return m.keyRepos(k)
	default:
		return m.keySettings(k)
	}
}

func (m model) keySessions(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		m.sessIdx = clamp(m.sessIdx-1, len(m.sessions))
		return m, nil
	case "down", "j":
		m.sessIdx = clamp(m.sessIdx+1, len(m.sessions))
		return m, nil
	case "n":
		if m.opts.RepoRoot == "" {
			m.fail(errors.New("new sessions come from a repo — run pier from inside one"))
			return m, nil
		}
		m.ov, m.input = ovNew, ""
		return m, nil
	}
	s, ok := m.current()
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "enter":
		return m.startAttach(s)
	case "d":
		q := "destroy " + s.Name + " and its disk?"
		if s.State == pier.StateFailed {
			q = "clear the failed create " + s.Name + "?" // nothing to destroy
		}
		return m.confirm(q, func() tea.Cmd {
			return act(func() doneMsg {
				err := m.be.Remove(context.Background(), s)
				return doneMsg{note: "removed " + s.Name, err: err, refresh: true, watch: true}
			})
		}), nil
	case "p":
		return m, act(func() doneMsg {
			err := m.be.Keep(context.Background(), s)
			return doneMsg{note: s.Name + " won't park when idle (the runaway cap still applies)", err: err}
		})
	case "m":
		if err := pier.CheckReady(s); err != nil {
			m.fail(err)
			return m, nil
		}
		m.machines = m.be.Machines(s)
		if len(m.machines) == 0 {
			m.fail(errors.New("no machine catalog here — `pier resize " + s.Name + " <type>` takes any type"))
			return m, nil
		}
		m.ov, m.pickIdx = ovResize, 0
		for i, mc := range m.machines {
			if mc.Type == s.InstanceType {
				m.pickIdx = i
			}
		}
		return m, nil
	case "l":
		return m.openLogs(s)
	}
	return m, nil
}

func (m model) current() (pier.Session, bool) {
	if len(m.sessions) == 0 {
		return pier.Session{}, false
	}
	return m.sessions[clamp(m.sessIdx, len(m.sessions))], true
}

// startAttach resumes a parked session first (blocking, off the UI loop),
// then hands the terminal to ssh+tmux.
func (m model) startAttach(s pier.Session) (tea.Model, tea.Cmd) {
	if m.attaching != "" {
		return m, nil
	}
	if err := pier.CheckReady(s); err != nil {
		m.fail(err)
		return m, nil
	}
	m.attaching, m.attachRetried = s.Name, false
	if s.State == pier.StateParked {
		m.note("resuming " + s.Name + " — about 20-60s, tmux comes back as you left it")
		return m, func() tea.Msg { return resumedMsg{s, m.be.Resume(context.Background(), s)} }
	}
	// Prove the VM answers while the app is still on screen: handing the
	// terminal to an ssh that's still connecting (or will fail) looks like
	// pier quit, and its errors land outside the app.
	m.note("connecting to " + s.Name + "…")
	return m, func() tea.Msg {
		return reachableMsg{s, m.be.WaitReachable(context.Background(), s, 90*time.Second)}
	}
}

func (m model) execAttach(s pier.Session) (tea.Model, tea.Cmd) {
	cmd, stop, err := m.be.Attach(context.Background(), s)
	if err != nil {
		m.attaching = ""
		m.fail(err)
		return m, nil
	}
	m.attachStart = time.Now()
	cmd = pier.ConnectingScreen(cmd, s.Name, false)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		stop() // the browser opener and port mirror end with the attach
		return attachDoneMsg{s, err}
	})
}

func (m model) confirm(q string, do func() tea.Cmd) model {
	m.ov, m.confirmQ, m.confirmDo = ovConfirm, q, do
	return m
}

func (m model) keyConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.ov = ovNone
	if k.String() == "y" || k.String() == "Y" {
		return m, m.confirmDo()
	}
	return m, nil
}

func (m model) keyNew(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.ov = ovNone
		return m, nil
	case tea.KeyEnter:
		branch := strings.TrimSpace(m.input)
		m.ov = ovNone
		if branch == "" {
			return m, nil
		}
		root := m.opts.RepoRoot
		return m, act(func() doneMsg {
			log, err := m.be.SpawnCreate(root, branch)
			return doneMsg{note: "creating " + branch + " in the background · log " + tilde(log), err: err, refresh: true, watch: true}
		})
	case tea.KeyBackspace:
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(k.Runes)
		return m, nil
	}
	return m, nil
}

func (m model) keyResize(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.ov = ovNone
	case "up", "k":
		m.pickIdx = clamp(m.pickIdx-1, len(m.machines))
	case "down", "j":
		m.pickIdx = clamp(m.pickIdx+1, len(m.machines))
	case "enter":
		m.ov = ovNone
		s, ok := m.current()
		if !ok {
			return m, nil
		}
		t := m.machines[m.pickIdx].Type
		if t == s.InstanceType {
			return m, nil
		}
		m.note("resizing " + s.Name + " to " + t + " — parks, resizes, resumes (~1-2 min)")
		return m, act(func() doneMsg {
			err := m.be.Resize(context.Background(), s, t)
			return doneMsg{note: s.Name + " is now a " + t, err: err, refresh: true}
		})
	}
	return m, nil
}

// --- repos ---------------------------------------------------------------------------

func (m model) currentRepo() (pier.Repo, bool) {
	if len(m.repos) == 0 {
		return pier.Repo{}, false
	}
	return m.repos[clamp(m.repoIdx, len(m.repos))], true
}

func (m model) keyRepos(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		m.repoIdx = clamp(m.repoIdx-1, len(m.repos))
		return m, nil
	case "down", "j":
		m.repoIdx = clamp(m.repoIdx+1, len(m.repos))
		return m, nil
	}
	r, ok := m.currentRepo()
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "b":
		if !r.Current {
			m.fail(errors.New("bake from inside the repo: cd to " + r.Name + " and run pier"))
			return m, nil
		}
		if pier.BakeRunning(r.Name) {
			m.note("a bake of " + r.Name + " is already running · log " + tilde(pier.BakeLogPath(r.Name)))
			return m, nil
		}
		q := "bake " + r.Name + "'s session image now? It runs in the background on a temporary instance."
		root := m.opts.RepoRoot
		return m.confirm(q, func() tea.Cmd {
			return act(func() doneMsg {
				log, err := m.be.SpawnBake(root)
				return doneMsg{note: "baking " + r.Name + " · log " + tilde(log), err: err, refresh: true}
			})
		}), nil
	case "+", "=":
		if !r.Current {
			m.fail(errors.New("the pool fills from the repo's checkout — run pier from inside " + r.Name))
			return m, nil
		}
		return m.setPoolSize(r, r.PoolSize+1)
	case "-", "_":
		if r.PoolSize == 0 {
			return m, nil
		}
		if !r.Current {
			m.fail(errors.New("set " + r.Name + "'s pool size from inside the repo; x drains it from anywhere"))
			return m, nil
		}
		return m.setPoolSize(r, r.PoolSize-1)
	case "f":
		if !r.Current {
			m.fail(errors.New("the pool fills from the repo's checkout — run pier from inside " + r.Name))
			return m, nil
		}
		if r.PoolSize == 0 {
			m.note(r.Name + " has no pool — + sets its size")
			return m, nil
		}
		root := m.opts.RepoRoot
		return m, act(func() doneMsg {
			log, err := m.be.SpawnFill(root)
			return doneMsg{note: "filling " + r.Name + "'s pool · log " + tilde(log), err: err, refresh: true, watch: true}
		})
	case "x":
		if r.Ready+r.Filling+r.Stale == 0 {
			m.note(r.Name + "'s pool is empty")
			return m, nil
		}
		return m.confirm("drain "+r.Name+"'s pool — destroy its parked sessions? (the pool size stays; the next create refills it)", func() tea.Cmd {
			return act(func() doneMsg {
				n, err := m.be.DrainPool(context.Background(), r.Name, nil)
				return doneMsg{note: "drained " + strconv.Itoa(n) + " from " + r.Name + "'s pool", err: err, refresh: true}
			})
		}), nil
	}
	return m, nil
}

func (m model) setPoolSize(r pier.Repo, n int) (tea.Model, tea.Cmd) {
	if n > 8 {
		return m, nil
	}
	root := r.Name
	if r.Current {
		root = m.opts.RepoRoot
	}
	cost := m.be.DiskMonthlyUSD()
	return m, act(func() doneMsg {
		_, err := m.be.SetPoolSize(context.Background(), root, n, nil)
		note := "pool off for " + r.Name
		if n > 0 {
			note = "pool size " + strconv.Itoa(n) + " for " + r.Name + " · each ~$" + strconv.Itoa(int(cost+0.5)) + "/mo parked · filling in the background"
		}
		return doneMsg{note: note, err: err, refresh: true, watch: n > 0}
	})
}

// --- settings --------------------------------------------------------------------------

func (m model) keySettings(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		m.setIdx = clamp(m.setIdx-1, len(m.settings))
		return m, nil
	case "down", "j":
		m.setIdx = clamp(m.setIdx+1, len(m.settings))
		return m, nil
	case "enter":
		if len(m.settings) == 0 {
			return m, nil
		}
		f := m.settings[m.setIdx]
		switch f.Kind {
		case config.KindChoice:
			m.pickOpts = f.Options
		case config.KindMachine:
			m.pickOpts = nil
			for _, mc := range m.be.MachineCatalog(f.Value) {
				m.pickOpts = append(m.pickOpts, config.Option{
					Value: mc.Type, Desc: mc.CPU + " vCPU · " + mc.Mem + " GB · " + mc.Cost,
				})
			}
			if len(m.pickOpts) == 0 {
				m.ov, m.input = ovEdit, f.Value
				return m, nil
			}
		default:
			m.ov, m.input = ovEdit, f.Value
			return m, nil
		}
		m.ov, m.pickIdx = ovPick, 0
		for i, o := range m.pickOpts {
			if o.Value == f.Value {
				m.pickIdx = i
			}
		}
		return m, nil
	}
	return m, nil
}

func (m model) keyPick(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.ov = ovNone
	case "up", "k":
		m.pickIdx = clamp(m.pickIdx-1, len(m.pickOpts))
	case "down", "j":
		m.pickIdx = clamp(m.pickIdx+1, len(m.pickOpts))
	case "enter":
		m.ov = ovNone
		return m.save(m.pickOpts[m.pickIdx].Value)
	}
	return m, nil
}

func (m model) keyEdit(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.ov = ovNone
	case tea.KeyEnter:
		return m.save(strings.TrimSpace(m.input))
	case tea.KeyBackspace:
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(k.Runes)
	}
	return m, nil
}

// save writes one setting. A rejected value keeps the editor open with the
// reason; an accepted one reloads the rows (a profile change moves two
// others) and, when it changes where cloud calls go, rebuilds the backend.
func (m model) save(val string) (tea.Model, tea.Cmd) {
	f := m.settings[m.setIdx]
	if err := m.be.Set(f.Key, val); err != nil {
		m.fail(err)
		return m, nil
	}
	m.ov = ovNone
	m.settings = m.be.Settings()
	m.setIdx = clamp(m.setIdx, len(m.settings))
	m.note(f.Label + " saved — applies to new sessions")
	if f.Key == "theme" {
		applyTheme(val)
		m.note("accent color saved")
	}
	switch {
	case f.Key == "driver", strings.HasPrefix(f.Key, "aws."), strings.HasPrefix(f.Key, "gcp."):
		return m, func() tea.Msg {
			be, err := m.opts.Open()
			return reopenedMsg{be, err}
		}
	case strings.HasPrefix(f.Key, "speed."):
		// Pool sizes derive from these: rebuild the Repos rows.
		m.repos = m.be.Repos(m.all, m.opts.RepoRoot)
	}
	return m, nil
}

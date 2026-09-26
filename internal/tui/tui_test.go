package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
)

// fake is a Backend with canned data that records what the app asked for.
type fake struct {
	sessions []pier.Session
	repos    []pier.Repo
	cfg      config.Config
	removed  []string
	kept     []string
	spawned  []string
	ready    map[string]int
	setErr   error
}

func newFake() *fake {
	now := time.Now()
	return &fake{
		cfg:   config.Default(),
		ready: map[string]int{},
		sessions: []pier.Session{
			{ID: "i-1", Name: "checkout-flow", Repo: "shop", Branch: "checkout-flow", State: pier.StateWorking, InstanceType: "t4g.medium", CostNote: "~$0.04/h", Created: now.Add(-2 * time.Hour)},
			{ID: "i-2", Name: "fix-login", Repo: "flb-estimation", Branch: "fix-login", State: pier.StateParked, InstanceType: "t4g.xlarge", CostNote: "~$7/mo", Created: now.Add(-26 * time.Hour)},
			{ID: "i-3", Name: "perf-test", Repo: "flb-estimation", State: pier.StateIdle, Setup: "running", InstanceType: "t4g.xlarge", CostNote: "~$0.13/h", Created: now.Add(-3 * time.Minute)},
			{Name: "broken", Repo: "shop", State: pier.StateFailed, FailReason: "scp: Connection closed", Created: now.Add(-time.Hour)},
			{ID: "i-4", Name: "pool-flb-1", Repo: "flb-estimation", State: pier.StateParked, PoolGen: "abc", Created: now.Add(-time.Hour)},
		},
		repos: []pier.Repo{
			{Name: "flb-estimation", Current: true, Image: "ami-1", Baked: &config.ImageInfo{BakedAt: now.Add(-62 * 24 * time.Hour), RepoIncluded: true},
				ReadyTarget: 1, Ready: 1, Sessions: 2, MonthlyUSD: 9.6,
				Reminders: []pier.Reminder{{Message: "flb-estimation's session image is 62 days old. Rebake so new sessions start with current dependencies.", Action: "pier bake"}}},
			{Name: "shop", Sessions: 1},
		},
	}
}

func (f *fake) Cloud() string { return "AWS eu-central-1" }
func (f *fake) Sessions(context.Context) ([]pier.Session, error) {
	return f.sessions, nil
}
func (f *fake) Repos([]pier.Session, string) []pier.Repo { return f.repos }
func (f *fake) Headroom(context.Context) (pier.Quota, error) {
	return pier.Quota{Detail: "10/32 vCPU"}, nil
}
func (f *fake) Remove(_ context.Context, s pier.Session) error {
	f.removed = append(f.removed, s.Name)
	return nil
}
func (f *fake) Keep(_ context.Context, s pier.Session) error {
	f.kept = append(f.kept, s.Name)
	return nil
}
func (f *fake) Resume(context.Context, pier.Session) error         { return nil }
func (f *fake) Resize(context.Context, pier.Session, string) error { return nil }
func (f *fake) Machines(pier.Session) []pier.Machine {
	return []pier.Machine{{Type: "t4g.medium", CPU: "2", Mem: "4", Cost: "~$0.03/h"}, {Type: "t4g.xlarge", CPU: "4", Mem: "16", Cost: "~$0.13/h"}}
}
func (f *fake) AttachCommand(context.Context, pier.Session) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}
func (f *fake) WaitReachable(context.Context, pier.Session, time.Duration) error { return nil }
func (f *fake) SetupLog(context.Context, pier.Session, int) (string, error) {
	return "\x1b[32m==> installing\x1b[0m\nprogress 10%\rprogress 100%\npier setup: done\n", nil
}
func (f *fake) SpawnCreate(root, branch string) (string, error) {
	f.spawned = append(f.spawned, branch)
	return "/tmp/create-" + branch + ".log", nil
}
func (f *fake) SpawnBake(string) (string, error) { return "/tmp/bake.log", nil }
func (f *fake) SetReady(_ context.Context, root string, n int, _ pier.Progress) (string, error) {
	f.ready[root] = n
	return "", nil
}
func (f *fake) DrainReady(context.Context, string, pier.Progress) (int, error) { return 0, nil }
func (f *fake) Settings() []pier.SettingValue {
	var out []pier.SettingValue
	for _, fl := range config.Settings {
		if !f.cfg.Visible(fl) {
			continue
		}
		v := config.Get(f.cfg, fl.Key)
		out = append(out, pier.SettingValue{Field: fl, Value: v, Display: fl.Display(v)})
	}
	return out
}
func (f *fake) Set(key, val string) error {
	if f.setErr != nil {
		return f.setErr
	}
	return config.Set(&f.cfg, key, val)
}
func (f *fake) MachineCatalog(string) []pier.Machine { return f.Machines(pier.Session{}) }
func (f *fake) ReauthCommand() *exec.Cmd             { return exec.Command("true") }
func (f *fake) DiskMonthlyUSD() float64              { return 7.6 }

// loaded returns a model that has received its first list at w×h.
func loaded(t *testing.T, f *fake, w, h int) model {
	t.Helper()
	m := newModel(Options{Open: func() (Backend, error) { return f, nil }, RepoRoot: "/code/flb-estimation", Version: "test"}, f)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(model)
	all, _ := f.Sessions(context.Background())
	next, _ = m.Update(sessionsMsg{all: all})
	return next.(model)
}

func press(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, c := m.Update(msg)
		m, cmd = next.(model), c
	}
	return m, cmd
}

// run executes a command chain until it yields a message the test cares
// about (doneMsg), feeding it back through Update.
func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for i := 0; cmd != nil && i < 5; i++ {
		msg := cmd()
		if _, ok := msg.(doneMsg); !ok {
			return m
		}
		next, c := m.Update(msg)
		m, cmd = next.(model), c
	}
	return m
}

// Ready sessions are inventory, not work: they never sit in the session
// table, but the header counts them.
func TestReadySessionsStayOutOfTheTable(t *testing.T) {
	m := loaded(t, newFake(), 140, 32)
	v := m.View()
	if strings.Contains(v, "pool-flb-1") {
		t.Error("a ready session must not render as a session row")
	}
	if !strings.Contains(v, "1 ready") || !strings.Contains(v, "checkout-flow") {
		t.Errorf("header must count ready sessions and the table must list real ones:\n%s", v)
	}
}

func TestNewSessionSpawnsInTheBackground(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 120, 30)
	m, _ = press(t, m, "n", "f", "e", "a", "t")
	if m.ov != ovNew || m.input != "feat" {
		t.Fatalf("n must open the branch prompt, got ov=%v input=%q", m.ov, m.input)
	}
	m, cmd := press(t, m, "enter")
	m = settle(t, m, cmd)
	if len(f.spawned) != 1 || f.spawned[0] != "feat" {
		t.Fatalf("enter must spawn a background create, got %v", f.spawned)
	}
	if !strings.Contains(m.status, "creating feat") || m.watch == 0 {
		t.Errorf("the app must say where the create went and poll until it shows, status=%q watch=%d", m.status, m.watch)
	}
}

func TestNewSessionNeedsARepo(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 120, 30)
	m.opts.RepoRoot = ""
	m, _ = press(t, m, "n")
	if m.ov == ovNew || !m.statusBad {
		t.Errorf("outside a repo, n must explain instead of prompting")
	}
}

func TestDeleteAsksFirst(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 120, 30)
	m, _ = press(t, m, "d")
	if m.ov != ovConfirm || !strings.Contains(m.confirmQ, "checkout-flow") {
		t.Fatalf("d must ask before destroying, got ov=%v q=%q", m.ov, m.confirmQ)
	}
	m, cmd := press(t, m, "n")
	if cmd != nil || len(f.removed) != 0 {
		t.Fatal("n must cancel")
	}
	m, _ = press(t, m, "d")
	m, cmd = press(t, m, "y")
	settle(t, m, cmd)
	if len(f.removed) != 1 || f.removed[0] != "checkout-flow" {
		t.Errorf("y must remove the selected session, got %v", f.removed)
	}
}

func TestFailedCreateClearsInsteadOfDestroying(t *testing.T) {
	m := loaded(t, newFake(), 120, 30)
	m, _ = press(t, m, "down", "down", "down", "d")
	if !strings.Contains(m.confirmQ, "clear the failed create broken") {
		t.Errorf("a failed create has nothing to destroy; got %q", m.confirmQ)
	}
}

func TestAttachRefusesWhatIsntReady(t *testing.T) {
	f := newFake()
	f.sessions[0].State = pier.StateCreating
	m := loaded(t, f, 120, 30)
	m, cmd := press(t, m, "enter")
	if cmd != nil || !m.statusBad || !strings.Contains(m.status, "still setting up") {
		t.Errorf("attaching mid-create must refuse cleanly, got status %q", m.status)
	}
}

func TestParkedAttachResumesFirst(t *testing.T) {
	m := loaded(t, newFake(), 120, 30)
	m, cmd := press(t, m, "down", "enter")
	if m.attaching != "fix-login" || cmd == nil {
		t.Fatal("enter on a parked session must start the resume")
	}
	if _, ok := cmd().(resumedMsg); !ok {
		t.Error("a parked attach must resume before handing over the terminal")
	}
}

func TestTabsAndRemindersBadge(t *testing.T) {
	m := loaded(t, newFake(), 140, 32)
	if !strings.Contains(m.header(), "•") {
		t.Error("a pending rebake reminder must badge the Repos tab")
	}
	m, _ = press(t, m, "tab")
	if m.tab != tabRepos {
		t.Fatal("tab must move to Repos")
	}
	v := m.View()
	for _, want := range []string{"flb-estimation", "prebuilt", "62 days old", "1/1"} {
		if !strings.Contains(v, want) {
			t.Errorf("Repos tab missing %q:\n%s", want, v)
		}
	}
}

func TestReadyCountChangesOnlyFromTheRepo(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 140, 32)
	m, _ = press(t, m, "tab")
	m, cmd := press(t, m, "+")
	settle(t, m, cmd)
	if f.ready["/code/flb-estimation"] != 2 {
		t.Errorf("+ on this repo must raise its ready count, got %v", f.ready)
	}
	m, _ = press(t, m, "down", "+")
	if !m.statusBad {
		t.Error("+ on another repo must explain that refills need its checkout")
	}
}

func TestSettingsGroupedAndSavable(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 120, 40)
	m, _ = press(t, m, "s")
	v := m.View()
	for _, want := range []string{"Cloud", "New sessions", "Idle & cost", "Speed", "park after", "ready sessions"} {
		if !strings.Contains(v, want) {
			t.Errorf("settings missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "gcp") || strings.Contains(v, "zone") {
		t.Error("only the active cloud's fields may show")
	}
	// Pick the speed profile row and choose Lean.
	for i, s := range m.settings {
		if s.Key == "speed.profile" {
			m.setIdx = i
		}
	}
	m, _ = press(t, m, "enter")
	if m.ov != ovPick {
		t.Fatal("enter on a choice must open the picker")
	}
	m, _ = press(t, m, "down", "enter")
	if f.cfg.Profile() != "lean" || f.cfg.Speed.ReadySessions != 0 {
		t.Errorf("choosing Lean must apply the preset, got %s ready=%d", f.cfg.Profile(), f.cfg.Speed.ReadySessions)
	}
}

func TestRejectedSettingKeepsTheEditorOpen(t *testing.T) {
	f := newFake()
	m := loaded(t, f, 120, 40)
	m, _ = press(t, m, "s")
	for i, s := range m.settings {
		if s.Key == "aws.subnet" {
			m.setIdx = i
		}
	}
	m, _ = press(t, m, "enter", "x", "y", "z", "enter")
	if m.ov != ovEdit || !m.statusBad {
		t.Errorf("a bad value must keep the editor open with the reason, ov=%v status=%q", m.ov, m.status)
	}
}

func TestLogViewerSanitizesAndFollows(t *testing.T) {
	m := loaded(t, newFake(), 120, 30)
	m, _ = press(t, m, "down", "down", "l")
	if m.ov != ovLogs {
		t.Fatal("l must open the log viewer")
	}
	next, _ := m.Update(m.fetchLog()())
	m = next.(model)
	v := m.View()
	if !strings.Contains(v, "progress 100%") || strings.Contains(v, "progress 10%") || !strings.Contains(v, "following") {
		t.Errorf("the viewer must show each line's final state and follow the tail:\n%s", v)
	}
}

func TestAuthExpiryOffersSignIn(t *testing.T) {
	m := loaded(t, newFake(), 120, 30)
	next, _ := m.Update(sessionsMsg{err: errors.New("aws sts get-caller-identity: Your session has expired. Please reauthenticate using 'aws login'.")})
	m = next.(model)
	if !m.authRequired || !strings.Contains(m.View(), "sign in") {
		t.Error("an expired login must offer to sign in again")
	}
}

// Screens render inside the terminal at common sizes: no line wider than
// the window, header on top, keys at the bottom.
func TestScreensFitTheWindow(t *testing.T) {
	for _, size := range [][2]int{{160, 40}, {100, 30}, {80, 24}} {
		for _, tb := range []tab{tabSessions, tabRepos, tabSettings} {
			m := loaded(t, newFake(), size[0], size[1])
			m.tab = tb
			v := m.View()
			lines := strings.Split(v, "\n")
			if len(lines) > size[1] {
				t.Errorf("%dx%d %s: %d lines, taller than the window", size[0], size[1], tabNames[tb], len(lines))
			}
			for i, l := range lines {
				if w := len([]rune(stripANSI(l))); w > size[0] {
					t.Errorf("%dx%d %s line %d is %d wide", size[0], size[1], tabNames[tb], i, w)
				}
			}
			if os.Getenv("PIER_TUI_DUMP") != "" {
				t.Logf("\n%s", v)
			}
		}
	}
}

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestSanitizeLog(t *testing.T) {
	// pnpm/docker progress spam: color codes plus \r-redrawn meters. Only
	// each line's final state should survive.
	in := "\x1b[32mpnpm install\x1b[0m\nprogress 1%\rprogress 50%\rprogress 100%\ndone\t✓\n\n\n"
	want := "pnpm install\nprogress 100%\ndone ✓"
	if got := sanitizeLog(in); got != want {
		t.Errorf("sanitizeLog:\n got %q\nwant %q", got, want)
	}
}

func TestWrapLines(t *testing.T) {
	got := wrapLines("abcdef\nx\n", 3)
	want := []string{"abc", "def", "x", ""}
	if len(got) != len(want) {
		t.Fatalf("want %d lines, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: want %q got %q", i, want[i], got[i])
		}
	}
}

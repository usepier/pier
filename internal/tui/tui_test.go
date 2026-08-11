package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kerem-kaynak/pier/internal/config"
	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/awsec2"
)

// View smoke tests: the load-flash bug was View rendering "0 sessions"
// before the first fetch landed, so pin the three distinct states.
func TestViewStates(t *testing.T) {
	m := model{loading: true}
	if v := m.View(); !strings.Contains(v, "fetching") {
		t.Errorf("pre-fetch view must show a loading line, not a session count:\n%s", v)
	}
	if v := m.View(); strings.Contains(v, "0 session") || strings.Contains(v, "no sessions") {
		t.Errorf("pre-fetch view leaked an empty-state:\n%s", v)
	}

	m.loaded = true
	if v := m.View(); !strings.Contains(v, "no sessions") {
		t.Errorf("loaded-empty view must say no sessions:\n%s", v)
	}

	m.sessions = []driver.Session{
		{Name: "fix-auth", Repo: "myapp", State: driver.StateWorking, Created: time.Now()},
		{Name: "big-build", Repo: "myapp", State: driver.StateRunning, Strained: true},
		{Name: "bad-deps", Repo: "myapp", State: driver.StateIdle, Setup: "failed"},
	}
	v := m.View()
	for _, want := range []string{"fix-auth", "big-build", "working", "strained", "(setup failed)", "NAME"} {
		if !strings.Contains(v, want) {
			t.Errorf("list view missing %q:\n%s", want, v)
		}
	}
}

// The settings page groups fields under human labels, shows the current
// values, surfaces the read-only "managed elsewhere" section, and explains
// the selected field in the detail footer.
func TestSettingsPage(t *testing.T) {
	cfg := config.Default()
	m := model{mode: modeSettings, cfg: &cfg}

	v := m.View()
	for _, want := range []string{
		"pier settings",         // title
		"session", "aws", "gcp", // group headers
		"auto-park", "machine", "connection", // human labels
		"t4g.medium",        // a current value
		"managed elsewhere", // read-only section
		"claude token",      // a read-only row
		"config: driver",    // detail footer for the selected (first) field
	} {
		if !strings.Contains(v, want) {
			t.Errorf("settings view missing %q:\n%s", want, v)
		}
	}
	// The raw TOML keys are gone from the rows — only the footer names them.
	if strings.Contains(v, "idle_timeout ") {
		t.Errorf("row still shows a raw config key instead of a label:\n%s", v)
	}
}

// A free-text field (gcp.project) opens the editor on enter and, on a bad
// value, stays in edit with an error without touching disk.
func TestSettingsTextValidation(t *testing.T) {
	cfg := config.Default()
	m := model{mode: modeSettings, cfg: &cfg, setIdx: fieldIndex(t, "gcp.project")}

	got, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if !m.editing {
		t.Fatalf("enter on a text field must open the editor, got editing=%v picking=%v", m.editing, m.picking)
	}
	m.setInput = "Bad_Project"
	got, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if !m.editing || !m.statusBad {
		t.Errorf("a bad project id must stay in edit with an error, got editing=%v status=%q", m.editing, m.status)
	}
	if cfg.GCP.Project != "" {
		t.Errorf("a rejected value leaked into config: %q", cfg.GCP.Project)
	}
}

// A choice field (auto-park / idle_timeout) opens a picker preselected on the
// current value; selecting a different option saves it.
func TestSettingsChoicePicker(t *testing.T) {
	cfg := config.Default() // idle_timeout defaults to 30m
	m := model{mode: modeSettings, cfg: &cfg, setIdx: fieldIndex(t, "idle_timeout")}

	got, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if !m.picking {
		t.Fatalf("enter on a choice field must open the picker, got picking=%v editing=%v", m.picking, m.editing)
	}
	if m.pickOpts[m.pickIdx].Value != "30m" {
		t.Errorf("picker must preselect the current value, got %q", m.pickOpts[m.pickIdx].Value)
	}
	if v := m.View(); !strings.Contains(v, "(current)") || !strings.Contains(v, "custom…") {
		t.Errorf("picker must mark the current option and offer custom…:\n%s", v)
	}
	// Move to a different option and select it.
	got, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	want := m.pickOpts[m.pickIdx].Value
	got, _ = m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if m.picking || cfg.IdleTimeout != want {
		t.Errorf("selecting an option must save it and close the picker, got picking=%v idle=%q want=%q", m.picking, cfg.IdleTimeout, want)
	}
}

// The machine field builds its picker from the injected catalog and converts
// each machine into an annotated option.
func TestSettingsMachinePicker(t *testing.T) {
	cfg := config.Default()
	m := model{mode: modeSettings, cfg: &cfg, setIdx: fieldIndex(t, "aws.instance_type"),
		opts: Options{SettingsMachines: func(driverID, cur string) []driver.Machine {
			return awsec2.Machines(cur)
		}}}
	got, _ := m.updateSettings(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if !m.picking || len(m.pickOpts) == 0 {
		t.Fatalf("enter on the machine field must open a populated picker, got picking=%v opts=%d", m.picking, len(m.pickOpts))
	}
	if m.pickOpts[m.pickIdx].Value != "t4g.medium" {
		t.Errorf("picker must preselect the configured type, got %q", m.pickOpts[m.pickIdx].Value)
	}
	if v := m.View(); !strings.Contains(v, "vCPU") || !strings.Contains(v, "GiB") {
		t.Errorf("machine picker must show specs:\n%s", v)
	}
}

// fieldIndex is the position of key in config.Settings, failing the test if
// the key was renamed out from under it.
func fieldIndex(t *testing.T, key string) int {
	t.Helper()
	for i, f := range config.Settings {
		if f.Key == key {
			return i
		}
	}
	t.Fatalf("no settable field %q", key)
	return 0
}

// The m key opens the resize picker preselected on the session's current
// type, and enter on that same type is a no-op notice, not a resize call.
func TestResizePicker(t *testing.T) {
	resized := ""
	m := model{loaded: true,
		sessions: []driver.Session{{Name: "fix-auth", Repo: "myapp", State: driver.StateRunning, InstanceType: "t4g.medium"}},
		opts: Options{
			Fetch:    func() ([]driver.Session, error) { return nil, nil },
			Resize:   func(s driver.Session, itype string) error { resized = itype; return nil },
			Machines: func(s driver.Session) []driver.Machine { return awsec2.Machines(s.InstanceType) },
		}}
	got, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = got.(model)
	if m.mode != modeResize {
		t.Fatalf("m must open the picker, got mode=%v status=%q", m.mode, m.status)
	}
	if cur := m.machines[m.machIdx].Type; cur != "t4g.medium" {
		t.Errorf("picker must preselect the current type, got %q", cur)
	}
	v := m.View()
	for _, want := range []string{"resize fix-auth", "t4g.large", "(current)", "vCPU", "GiB"} {
		if !strings.Contains(v, want) {
			t.Errorf("picker view missing %q:\n%s", want, v)
		}
	}

	// Enter on the current type: notice, no resize.
	got, cmd := m.updateResize(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if cmd != nil || resized != "" {
		t.Errorf("enter on the current type must not resize, got resized=%q", resized)
	}
	if m.mode != modeList || !strings.Contains(m.status, "already") {
		t.Errorf("want an already-that-size notice back on the list, got mode=%v status=%q", m.mode, m.status)
	}

	// Reopen, move down one, confirm: the resize command fires.
	got, _ = m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = got.(model)
	got, _ = m.updateResize(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(model)
	want := m.machines[m.machIdx].Type
	got, cmd = m.updateResize(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if cmd == nil {
		t.Fatal("enter on a different type must return the resize command")
	}
	if batch, ok := cmd().(tea.BatchMsg); ok { // Batch wraps, doesn't run
		for _, c := range batch {
			c()
		}
	}
	if resized != want {
		t.Errorf("resize called with %q, want %q", resized, want)
	}
	if !m.loading || !strings.Contains(m.status, "resizing fix-auth") {
		t.Errorf("want spinner + resizing status, got loading=%v status=%q", m.loading, m.status)
	}
}

// Enter on a still-creating session must not spawn ssh — mid-create attaches
// used to dump a raw TargetNotConnected. It shows a notice and stays put
// instead.
func TestEnterOnCreatingSession(t *testing.T) {
	m := model{loaded: true,
		opts: Options{Attach: func(driver.Session) (*exec.Cmd, error) {
			t.Fatal("attach spawned for a creating session")
			return nil, nil
		}},
		sessions: []driver.Session{
			{Name: "half-built", Repo: "myapp", State: driver.StateCreating},
		}}
	got, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter on a creating session must not attach")
	}
	gm := got.(model)
	if gm.attachSess.Name != "" {
		t.Error("attach in flight for a creating session")
	}
	if !strings.Contains(gm.status, "still setting up") || gm.statusBad {
		t.Errorf("want a friendly notice, got status=%q bad=%v", gm.status, gm.statusBad)
	}
}

func TestLogsKey(t *testing.T) {
	m := model{loaded: true,
		opts: Options{FetchLog: func(driver.Session) (string, error) { return "pier setup: done", nil }},
		sessions: []driver.Session{
			{Name: "half-built", Repo: "myapp", State: driver.StateCreating},
		}}
	got, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd != nil {
		t.Fatal("l on a creating session must do nothing — there is no log yet")
	}
	gm := got.(model)
	if !strings.Contains(gm.status, "still setting up") || gm.statusBad {
		t.Errorf("want a friendly notice, got status=%q bad=%v", gm.status, gm.statusBad)
	}

	// A parked session must not resume as a side effect of opening logs.
	m.sessions[0].State = driver.StateParked
	got, cmd = m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd != nil {
		t.Fatal("l on a parked session must not fire a fetch — that would resume the VM")
	}
	gm = got.(model)
	if !strings.Contains(gm.status, "pier logs half-built") {
		t.Errorf("want the notice to point at the CLI, got %q", gm.status)
	}

	m.sessions[0].State = driver.StateRunning
	got, cmd = m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if cmd == nil {
		t.Fatal("l on a running session must fire the log fetch")
	}
	gm = got.(model)
	if gm.mode != modeLogs || gm.logSess.Name != "half-built" {
		t.Errorf("want modeLogs on half-built, got mode=%v session=%q", gm.mode, gm.logSess.Name)
	}
	if !gm.logStick || !gm.logLoading {
		t.Errorf("the viewer must open following the tail while it fetches, got stick=%v loading=%v", gm.logStick, gm.logLoading)
	}

	// esc goes back to the list with the sessions intact.
	got, _ = gm.updateLogs(tea.KeyMsg{Type: tea.KeyEsc})
	gm = got.(model)
	if gm.mode != modeList || len(gm.sessions) != 1 {
		t.Errorf("esc must return to the list, got mode=%v sessions=%d", gm.mode, len(gm.sessions))
	}
}

// Enter on a running session hands the terminal to ssh via ExecProcess —
// the TUI must survive the attach (no tea.Quit), so a detach lands back on
// the list.
func TestEnterAttachesWithoutQuitting(t *testing.T) {
	attached := 0
	m := model{loaded: true,
		opts: Options{Attach: func(driver.Session) (*exec.Cmd, error) {
			attached++
			return exec.Command("true"), nil
		}},
		sessions: []driver.Session{
			{Name: "fix-auth", Repo: "myapp", State: driver.StateRunning},
		}}
	got, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	gm := got.(model)
	if attached != 1 || cmd == nil {
		t.Fatalf("enter must build and run the attach command, got attached=%d cmd=%v", attached, cmd)
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Error("enter must not quit the TUI to attach")
	}
	if gm.attachSess.Name != "fix-auth" {
		t.Errorf("attachSess must mark the attach in flight, got %q", gm.attachSess.Name)
	}
	if !strings.Contains(gm.status, "C-b d") {
		t.Errorf("the detach hint must show in the TUI, got status=%q", gm.status)
	}

	// A second enter while the attach is in flight is a no-op.
	if _, cmd := gm.updateList(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || attached != 1 {
		t.Errorf("enter during an attach must do nothing, got attached=%d", attached)
	}
}

// Enter on a parked session resumes it inside the TUI first, then attaches;
// a failed resume clears the in-flight state with an error status.
func TestEnterOnParkedResumesFirst(t *testing.T) {
	resumed, attached := 0, 0
	m := model{loaded: true,
		opts: Options{
			Resume: func(driver.Session) error { resumed++; return nil },
			Attach: func(driver.Session) (*exec.Cmd, error) {
				attached++
				return exec.Command("true"), nil
			},
		},
		sessions: []driver.Session{
			{Name: "fix-auth", Repo: "myapp", State: driver.StateParked},
		}}
	got, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	gm := got.(model)
	if attached != 0 || !strings.Contains(gm.status, "resuming") || !gm.loading {
		t.Fatalf("enter on parked must resume before attaching, got attached=%d status=%q", attached, gm.status)
	}
	var rm tea.Msg
	if batch, ok := cmd().(tea.BatchMsg); ok { // Batch wraps, doesn't run
		for _, c := range batch {
			if msg, ok := c().(resumedMsg); ok {
				rm = msg
			}
		}
	}
	if resumed != 1 || rm == nil {
		t.Fatalf("the batch must run the resume, got resumed=%d msg=%v", resumed, rm)
	}
	got, cmd = gm.Update(rm)
	gm = got.(model)
	if attached != 1 || cmd == nil || gm.attachSess.Name != "fix-auth" {
		t.Errorf("a finished resume must attach, got attached=%d cmd=%v sess=%q", attached, cmd, gm.attachSess.Name)
	}

	// A failed resume surfaces the error and clears the in-flight state.
	got, _ = gm.Update(resumedMsg{gm.attachSess, errors.New("quota exceeded")})
	gm = got.(model)
	if !gm.statusBad || gm.attachSess.Name != "" {
		t.Errorf("a failed resume must error and clear the attach, got status=%q sess=%q", gm.status, gm.attachSess.Name)
	}
}

// A fast transport failure gets exactly one wait-for-reachability and retry,
// mirroring the CLI attach loop; a second failure lands an error on the list.
func TestAttachRetriesOnceThenFails(t *testing.T) {
	waited, attached := 0, 0
	s := driver.Session{Name: "fix-auth", Repo: "myapp", State: driver.StateRunning}
	m := model{loaded: true, sessions: []driver.Session{s},
		attachSess: s, attachStart: time.Now(),
		opts: Options{
			Fetch:         func() ([]driver.Session, error) { return nil, nil },
			RetryAttach:   func(error, time.Duration) bool { return true },
			WaitReachable: func(driver.Session) error { waited++; return nil },
			Attach: func(driver.Session) (*exec.Cmd, error) {
				attached++
				return exec.Command("true"), nil
			},
		}}
	got, cmd := m.Update(attachDoneMsg{s, errors.New("exit status 255")})
	gm := got.(model)
	if !gm.attachRetried || !strings.Contains(gm.status, "not reachable yet") {
		t.Fatalf("a retryable failure must wait for reachability, got status=%q", gm.status)
	}
	var rm tea.Msg
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if msg, ok := c().(reachableMsg); ok {
				rm = msg
			}
		}
	}
	if waited != 1 || rm == nil {
		t.Fatalf("the batch must run the reachability wait, got waited=%d msg=%v", waited, rm)
	}
	got, cmd = gm.Update(rm)
	gm = got.(model)
	if attached != 1 || cmd == nil {
		t.Fatalf("a reachable session must attach again, got attached=%d cmd=%v", attached, cmd)
	}

	// The second failure is final: error status, no more waits.
	got, _ = gm.Update(attachDoneMsg{s, errors.New("exit status 255")})
	gm = got.(model)
	if !gm.statusBad || gm.attachSess.Name != "" || waited != 1 {
		t.Errorf("a second failure must give up, got status=%q sess=%q waited=%d", gm.status, gm.attachSess.Name, waited)
	}
}

// Detaching (ssh exiting 0) lands back on the list with a notice and a
// fresh session fetch — never at the shell.
func TestDetachReturnsToList(t *testing.T) {
	s := driver.Session{Name: "fix-auth", Repo: "myapp", State: driver.StateRunning}
	m := model{loaded: true, sessions: []driver.Session{s}, attachSess: s,
		opts: Options{Fetch: func() ([]driver.Session, error) { return []driver.Session{s}, nil }}}
	got, cmd := m.Update(attachDoneMsg{s, nil})
	gm := got.(model)
	if !strings.Contains(gm.status, "detached from fix-auth") || gm.statusBad {
		t.Errorf("want a detach notice, got status=%q bad=%v", gm.status, gm.statusBad)
	}
	if gm.attachSess.Name != "" {
		t.Errorf("the attach must no longer be in flight, got %q", gm.attachSess.Name)
	}
	fetched := false
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if _, ok := c().(sessionsMsg); ok {
				fetched = true
			}
		}
	}
	if !fetched {
		t.Error("a detach must refresh the session list")
	}
}

// On the alternate screen the renderer keeps the LAST height lines when the
// view overflows, which would scroll the header off — the table must window
// its rows around the cursor instead.
func TestTableWindowFollowsCursor(t *testing.T) {
	m := model{loaded: true, height: 15}
	for i := range 40 {
		m.sessions = append(m.sessions, driver.Session{
			Name: fmt.Sprintf("s%d", i), Repo: "myapp", State: driver.StateRunning,
		})
	}

	v := m.View()
	if !strings.Contains(v, "s0") || strings.Contains(v, "s39") {
		t.Errorf("cursor at the top must show the first rows:\n%s", v)
	}
	if c := strings.Count(v, "\n"); c > m.height {
		t.Errorf("view is %d lines for a %d-line terminal:\n%s", c, m.height, v)
	}
	if !strings.Contains(v, "NAME") || !strings.Contains(v, "quit") {
		t.Errorf("header and footer must survive the windowing:\n%s", v)
	}

	m.cursor = 39
	v = m.View()
	if !strings.Contains(v, "s39") || strings.Contains(v, "s0 ") {
		t.Errorf("the window must follow the cursor to the bottom:\n%s", v)
	}

	// No WindowSizeMsg yet (height 0): render everything, as inline tests do.
	m.height, m.cursor = 0, 0
	v = m.View()
	if !strings.Contains(v, "s0") || !strings.Contains(v, "s39") {
		t.Errorf("zero height must render all rows:\n%s", v)
	}
}

// FoldPool classifies members the way reconcile would; the generation check
// applies only to the repo whose local checkout produced curGen — other
// repos' gens are unknowable here.
func TestFoldPool(t *testing.T) {
	now := time.Now()
	ss := []driver.Session{
		{Repo: "shop", PoolGen: "aaa", State: driver.StateParked, Created: now.Add(-time.Hour)},
		{Repo: "shop", PoolGen: "bbb", State: driver.StateParked, Created: now}, // wrong gen
		{Repo: "shop", PoolGen: "aaa", State: driver.StateCreating, Created: now},
		{Repo: "shop", PoolGen: "aaa", State: driver.StateDead, Created: now},
		{Repo: "shop", PoolGen: "aaa", State: driver.StateDeleting, Created: now},
		{Repo: "shop", State: driver.StateRunning, Created: now}, // a real session
		{Repo: "tools", PoolGen: "zzz", State: driver.StateParked, Created: now},
	}
	if st := FoldPool(ss, "shop", "shop", "aaa", 0, now); st.Ready != 1 || st.Filling != 1 || st.Stale != 2 {
		t.Errorf("shop = %+v, want 1 ready, 1 filling, 2 stale", st)
	}
	if st := FoldPool(ss, "tools", "shop", "aaa", 0, now); st.Ready != 1 || st.Stale != 0 {
		t.Errorf("tools = %+v, want its member ready (gen unknowable from here)", st)
	}

	// Age judgments: a parked member past max_age is stale even for repos
	// whose gen is unknowable, and an unparked member past the fill grace is
	// a corpse, not "filling" — its fill died hours ago.
	old := []driver.Session{
		{Repo: "tools", PoolGen: "zzz", State: driver.StateParked, Created: now.Add(-30 * 24 * time.Hour)},
		{Repo: "tools", PoolGen: "zzz", State: driver.StateCreating, Created: now.Add(-3 * time.Hour)},
	}
	if st := FoldPool(old, "tools", "", "", 14*24*time.Hour, now); st.Ready != 0 || st.Filling != 0 || st.Stale != 2 {
		t.Errorf("old members = %+v, want both stale (over-age + grace-blown)", st)
	}
	if st := FoldPool(old[:1], "tools", "", "", 0, now); st.Ready != 1 {
		t.Errorf("maxAge 0 = %+v, want the age check skipped", st)
	}
}

// The w page is the primary pool surface: members split out of the session
// list (never as table rows), the header counts them, and the page shows
// per-repo warmth with what it costs.
func TestPoolPage(t *testing.T) {
	if got, _ := (model{loaded: true}).updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")}); got.(model).mode != modeList {
		t.Error("w without pool hooks wired must be a no-op")
	}

	now := time.Now()
	m := model{loaded: true, opts: Options{
		PoolSizes:   func() map[string]int { return map[string]int{"shop": 2} },
		CurrentRepo: "shop",
		CurrentGen:  "aaaaaaaaaaaa",
	}}
	got, _ := m.Update(sessionsMsg{sessions: []driver.Session{
		{Name: "fix-auth", Repo: "shop", State: driver.StateRunning, Created: now},
		{Name: "pool-shop-a1b2", Repo: "shop", State: driver.StateParked, PoolGen: "aaaaaaaaaaaa", Created: now.Add(-time.Hour)},
		{Name: "pool-shop-c3d4", Repo: "shop", State: driver.StateCreating, PoolGen: "aaaaaaaaaaaa", Created: now},
		{Name: "pool-tools-e5f6", Repo: "tools", State: driver.StateParked, PoolGen: "bbbbbbbbbbbb", Created: now},
	}})
	m = got.(model)

	v := m.View()
	if strings.Contains(v, "pool-shop-a1b2") {
		t.Errorf("pool members must never render as session rows:\n%s", v)
	}
	if !strings.Contains(v, "1 session(s)") || !strings.Contains(v, "3 warm") {
		t.Errorf("header must count sessions and warm members separately:\n%s", v)
	}

	got, _ = m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = got.(model)
	if m.mode != modePool {
		t.Fatalf("w must open the pool page, got mode=%v", m.mode)
	}
	if m.poolRepos[m.poolIdx] != "shop" {
		t.Errorf("cursor must land on the current repo, got %q", m.poolRepos[m.poolIdx])
	}
	v = m.View()
	for _, want := range []string{"pier pools", "shop", "1 ready", "1 filling", "tools", "$3-4/mo", "this repo"} {
		if !strings.Contains(v, want) {
			t.Errorf("pool page missing %q:\n%s", want, v)
		}
	}
	// tools has a member but no configured pool — that's money leaking.
	if !strings.Contains(v, "orphaned") {
		t.Errorf("members without a configured pool must show as orphaned:\n%s", v)
	}
}

// Sizing works only on the current repo's row, stages with +/- (rendered as
// from→to), applies on enter through PoolSet, and esc cancels the staging
// before it leaves the page.
func TestPoolSizeEditing(t *testing.T) {
	applied := -1
	sizes := map[string]int{"shop": 1}
	m := model{loaded: true, mode: modePool,
		poolSizes: sizes, poolRepos: []string{"other", "shop"}, poolIdx: 1, poolPend: -1,
		members: []driver.Session{{Name: "pool-other-x", Repo: "other", State: driver.StateParked, PoolGen: "g", Created: time.Now()}},
		opts: Options{
			CurrentRepo: "shop",
			PoolSizes:   func() map[string]int { return sizes },
			Fetch:       func() ([]driver.Session, error) { return nil, nil },
			PoolSet:     func(size int) (string, error) { applied = size; return "/tmp/pool-shop.log", nil },
		}}

	for range 2 {
		got, _ := m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
		m = got.(model)
	}
	if m.poolPend != 3 || applied != -1 {
		t.Fatalf("+ must stage, not apply: pend=%d applied=%d", m.poolPend, applied)
	}
	if v := m.View(); !strings.Contains(v, "1→3") {
		t.Errorf("a staged size must render as from→to:\n%s", v)
	}

	got, _ := m.updatePool(tea.KeyMsg{Type: tea.KeyEsc})
	m = got.(model)
	if m.poolPend != -1 || m.mode != modePool {
		t.Fatalf("first esc must only cancel the staged size, got pend=%d mode=%v", m.poolPend, m.mode)
	}

	got, _ = m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	m = got.(model)
	got, cmd := m.updatePool(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if cmd == nil {
		t.Fatal("enter on a staged size must return the apply command")
	}
	var act tea.Msg
	if batch, ok := cmd().(tea.BatchMsg); ok { // Batch wraps, doesn't run
		for _, c := range batch {
			if msg, ok := c().(poolActMsg); ok {
				act = msg
			}
		}
	}
	if applied != 2 || act == nil {
		t.Fatalf("apply must call PoolSet with the staged size, got applied=%d msg=%v", applied, act)
	}
	got, _ = m.Update(act)
	m = got.(model)
	if !strings.Contains(m.status, "pool size 2") || m.statusBad {
		t.Errorf("a landed apply must confirm with the log path, got status=%q", m.status)
	}

	// Another repo's row can't be sized from here — its checkout is elsewhere.
	m.poolIdx, m.poolPend = 0, -1
	got, _ = m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	m = got.(model)
	if m.poolPend != -1 || !strings.Contains(m.status, "inside other") {
		t.Errorf("sizing another repo must be refused with a pointer, got pend=%d status=%q", m.poolPend, m.status)
	}
}

// Draining destroys money-costing VMs, so d asks y/n first; it works for any
// repo (no checkout needed) but refuses politely when there's nothing there.
func TestPoolDrainConfirm(t *testing.T) {
	drained := ""
	m := model{loaded: true, mode: modePool,
		poolRepos: []string{"old"}, poolPend: -1,
		members: []driver.Session{{Name: "pool-old-x", Repo: "old", State: driver.StateParked, PoolGen: "g", Created: time.Now()}},
		opts: Options{
			PoolSizes: func() map[string]int { return nil },
			Fetch:     func() ([]driver.Session, error) { return nil, nil },
			PoolDrain: func(repo string) (int, error) { drained = repo; return 1, nil },
		}}
	got, _ := m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = got.(model)
	if !m.poolConfirm {
		t.Fatal("d on a repo with members must ask for confirmation")
	}
	if v := m.View(); !strings.Contains(v, "drain old") || !strings.Contains(v, "(y/n)") {
		t.Errorf("the confirm prompt must name the repo:\n%s", v)
	}
	got, _ = m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = got.(model)
	if m.poolConfirm || drained != "" {
		t.Fatalf("n must cancel the drain, got confirm=%v drained=%q", m.poolConfirm, drained)
	}

	got, _ = m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = got.(model)
	got, cmd := m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = got.(model)
	if cmd == nil {
		t.Fatal("y must fire the drain")
	}
	var act tea.Msg
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if msg, ok := c().(poolActMsg); ok {
				act = msg
			}
		}
	}
	if drained != "old" || act == nil {
		t.Fatalf("drain must target the selected repo, got %q", drained)
	}
	got, _ = m.Update(act)
	m = got.(model)
	if !strings.Contains(m.status, "drained 1 member") {
		t.Errorf("a landed drain must report the count, got %q", m.status)
	}

	m.members, m.poolConfirm, m.status = nil, false, ""
	got, _ = m.updatePool(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = got.(model)
	if m.poolConfirm || !strings.Contains(m.status, "no warm members") {
		t.Errorf("d on an empty pool must notice, not prompt, got confirm=%v status=%q", m.poolConfirm, m.status)
	}
}

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

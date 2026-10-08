package main

// The session's agent, as pier launched it: claude or codex in a tmux
// window of its own, followed through the hooks pier gives it. Frontends
// read its state (working, waiting on you, done), answer its permission
// requests, and send it prompts — queued here, on the session's disk, and
// typed into its pane when it is ready for them, so a prompt sent to a
// parked session is delivered once it wakes, whoever is still around.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Agent states.
const (
	agentStarting = "starting" // launched, not yet ready for a prompt
	agentReady    = "ready"    // at its prompt, nothing asked
	agentWorking  = "working"  // a turn is running
	agentWaiting  = "waiting"  // stopped mid-turn on the human: a permission, a question
	agentEnded    = "ended"    // the program exited
)

// agentSpec is what to launch, written by the create (or claim) that made
// the session: ~/.pier/agent/spec.json.
type agentSpec struct {
	ID             string `json:"id"`   // this launch; a claim brings a new one
	Kind           string `json:"kind"` // claude | codex
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Conversation   string `json:"conversation,omitempty"` // claude session id, chosen up front
}

// agentState is one agent's live state, by tmux pane.
type agentState struct {
	Pane         string    `json:"pane"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	Waiting      string    `json:"waiting,omitempty"` // permission | input
	Conversation string    `json:"conversation,omitempty"`
	Transcript   string    `json:"transcript,omitempty"`
	Model        string    `json:"model,omitempty"`
	Since        time.Time `json:"since"`
	Activity     time.Time `json:"activity"` // last sign of life: a hook, a delivery
	Last         string    `json:"last,omitempty"`
	Pending      *pending  `json:"pending,omitempty"`
}

// pending is a permission request waiting on a frontend.
type pending struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
	At    time.Time       `json:"at"`

	decided chan decision
}

type decision struct {
	Behavior     string          `json:"behavior"` // allow | deny | "" (release: let the agent ask in its terminal)
	Message      string          `json:"message,omitempty"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

// permissionWait bounds how long a request waits on a frontend before the
// agent asks in its own terminal.
var permissionWait = 120 * time.Second

func agentDir(home string) string { return filepath.Join(home, ".pier", "agent") }

type agents struct {
	mu     sync.Mutex
	byPane map[string]*agentState
	home   string
	notify func() // the state changed: publish it
}

// savedAgents is state.json: the agents, and the boot they ran in.
type savedAgents struct {
	Boot   string       `json:"boot"`
	Agents []agentState `json:"agents"`
}

// bootID names this boot of the machine; a var so tests can reboot.
var bootID = func() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func newAgents(home string, notify func()) *agents {
	a := &agents{byPane: map[string]*agentState{}, home: home, notify: notify}
	b, err := os.ReadFile(filepath.Join(agentDir(home), "state.json"))
	if err != nil {
		return a
	}
	var saved savedAgents
	if json.Unmarshal(b, &saved) != nil {
		return a
	}
	sameBoot := saved.Boot != "" && saved.Boot == bootID()
	for _, s := range saved.Agents {
		if s.Pending != nil {
			// The hook that asked lost its supervisor: the agent asks in
			// its terminal now.
			s.Pending, s.Waiting = nil, "input"
		}
		if !sameBoot {
			// A boot ended every process: what was running is gone.
			if s.State == agentWorking || s.State == agentWaiting {
				s.Last = "interrupted when the session parked"
			}
			s.State, s.Waiting = agentEnded, ""
		}
		st := s
		a.byPane[s.Pane] = &st
	}
	return a
}

// snapshot copies every agent's state, by pane order.
func (a *agents) snapshot() []agentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]agentState, 0, len(a.byPane))
	for _, s := range a.byPane {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pane < out[j].Pane })
	return out
}

// changed persists the states and tells the hub. Called without mu.
func (a *agents) changed() {
	b, _ := json.Marshal(savedAgents{Boot: bootID(), Agents: a.snapshot()})
	_ = os.MkdirAll(agentDir(a.home), 0o700)
	_ = writeAtomic(filepath.Join(agentDir(a.home), "state.json"), b)
	if a.notify != nil {
		a.notify()
	}
}

// primary is the agent pier launched (the newest one seen), the one prompts
// go to.
func (a *agents) primary() *agentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	var p *agentState
	for _, s := range a.byPane {
		if s.State == agentEnded {
			continue
		}
		if p == nil || s.Since.After(p.Since) {
			p = s
		}
	}
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// conversation is the conversation a pane's agent is on, for restore.
func (a *agents) conversation(pane string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.byPane[pane]; s != nil {
		return s.Conversation
	}
	return ""
}

// turnOpen reports a turn in progress: the session is busy.
func (a *agents) turnOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.byPane {
		// A turn with no sign of life for 20 minutes is a stuck agent, not
		// work: let the idle clock run.
		if s.State == agentWorking && time.Since(s.Activity) < 20*time.Minute {
			return true
		}
	}
	return false
}

// hook applies one hook event to its pane's state.
func (a *agents) hook(kind, event, pane string, payload map[string]any) {
	str := func(k string) string { v, _ := payload[k].(string); return v }
	a.mu.Lock()
	s := a.byPane[pane]
	if s == nil {
		if pane == "" {
			a.mu.Unlock()
			return
		}
		s = &agentState{Pane: pane, Kind: kind, State: agentStarting, Since: time.Now()}
		a.byPane[pane] = s
	}
	now := time.Now()
	s.Activity = now
	if v := str("session_id"); v != "" {
		s.Conversation = v
	} else if v := str("thread-id"); v != "" { // codex
		s.Conversation = v
	}
	if v := str("transcript_path"); v != "" {
		s.Transcript = v
	}
	if v := str("model"); v != "" {
		s.Model = v
	}
	set := func(state, waiting string) {
		if s.State != state {
			s.Since = now
		}
		s.State, s.Waiting = state, waiting
	}
	switch event {
	case "SessionStart":
		set(agentReady, "")
	case "UserPromptSubmit":
		set(agentWorking, "")
	case "Stop", "notify": // codex reports a finished turn through notify
		set(agentReady, "")
		if v := str("last_assistant_message"); v != "" {
			s.Last = truncate(v, 400)
		} else if v := str("last-assistant-message"); v != "" {
			s.Last = truncate(v, 400)
		}
	case "Notification":
		if t := str("notification_type"); t == "idle_prompt" {
			set(agentReady, "")
		} else if s.State == agentWorking || t == "permission_prompt" || t == "agent_needs_input" {
			set(agentWaiting, "input")
		}
	case "SessionEnd":
		set(agentEnded, "")
	}
	a.mu.Unlock()
	a.changed()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// requestPermission records a permission request and waits for a frontend's
// answer — but only while a frontend that can answer is connected. It
// releases (an empty decision: the agent asks in its terminal) when none
// is, when the last one goes, when someone attaches to the agent's window,
// or after permissionWait.
func (a *agents) requestPermission(pane, tool string, input json.RawMessage, answerable func() bool) decision {
	if !answerable() {
		return decision{}
	}
	p := &pending{ID: newOpID(), Tool: tool, Input: input, At: time.Now(), decided: make(chan decision, 1)}
	a.mu.Lock()
	s := a.byPane[pane]
	if s == nil {
		s = &agentState{Pane: pane, Kind: "claude", Since: time.Now()}
		a.byPane[pane] = s
	}
	s.Pending, s.State, s.Waiting, s.Activity = p, agentWaiting, "permission", time.Now()
	a.mu.Unlock()
	a.changed()

	var d decision
	select {
	case d = <-p.decided:
	case <-time.After(permissionWait):
	}
	a.mu.Lock()
	if s.Pending == p {
		s.Pending = nil
		if d.Behavior == "allow" {
			s.State, s.Waiting = agentWorking, ""
		}
	}
	a.mu.Unlock()
	a.changed()
	return d
}

// decide answers a pending permission request; false when it is gone.
func (a *agents) decide(id string, d decision) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.byPane {
		if s.Pending != nil && s.Pending.ID == id {
			s.Pending.decided <- d
			return true
		}
	}
	return false
}

// releaseAll lets every pending request fall back to the agent's own
// terminal: the last frontend that could answer is gone.
func (a *agents) releaseAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.byPane {
		if s.Pending != nil {
			select {
			case s.Pending.decided <- decision{}:
			default:
			}
		}
	}
}

// releaseAttached releases requests of agents whose window someone is now
// attached to: they answer in the terminal.
func (a *agents) releaseAttached(attachedWindows map[string]bool) {
	a.mu.Lock()
	var panes []string
	for pane, s := range a.byPane {
		if s.Pending != nil {
			panes = append(panes, pane)
		}
	}
	a.mu.Unlock()
	for _, pane := range panes {
		out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{window_id}").Output()
		if err != nil || !attachedWindows[strings.TrimSpace(string(out))] {
			continue
		}
		a.mu.Lock()
		if s := a.byPane[pane]; s != nil && s.Pending != nil {
			select {
			case s.Pending.decided <- decision{}:
			default:
			}
		}
		a.mu.Unlock()
	}
}

// --- prompts ---------------------------------------------------------------------

// prompt is one queued prompt: ~/.pier/agent/queue/<id>.json until it is
// typed in, then sent/<id>.json.
type prompt struct {
	ID    string    `json:"id"`
	Text  string    `json:"text"`
	Mode  string    `json:"mode,omitempty"` // queue (default): when the agent is free; now: right away
	At    time.Time `json:"at"`
	Tries int       `json:"tries,omitempty"`
	// First marks a create's first prompt: it waits for setup to finish.
	First bool `json:"first,omitempty"`
}

var promptID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// enqueue adds a prompt, once per id.
func enqueue(home string, p prompt) error {
	if !promptID.MatchString(p.ID) {
		return fmt.Errorf("bad prompt id %q", p.ID)
	}
	dir := agentDir(home)
	for _, sub := range []string{"queue", "sent"} {
		if _, err := os.Stat(filepath.Join(dir, sub, p.ID+".json")); err == nil {
			return nil // already queued or delivered
		}
	}
	if p.At.IsZero() {
		p.At = time.Now()
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "queue"), 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "queue", p.ID+".json"), b)
}

// queued lists the prompts waiting, oldest first.
func queued(home string) []prompt {
	files, _ := filepath.Glob(filepath.Join(agentDir(home), "queue", "*.json"))
	var out []prompt
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p prompt
		if json.Unmarshal(b, &p) == nil && p.ID != "" {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func markSent(home string, p prompt) {
	dir := agentDir(home)
	_ = os.MkdirAll(filepath.Join(dir, "sent"), 0o700)
	_ = os.Rename(filepath.Join(dir, "queue", p.ID+".json"), filepath.Join(dir, "sent", p.ID+".json"))
}

// sanitize keeps a prompt to text: an escape sequence pasted into the pane
// could end the bracketed paste and run the rest as keystrokes.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == 0x1b || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// typePrompt pastes a prompt into a pane and submits it.
func typePrompt(pane, text string) error {
	load := exec.Command("tmux", "load-buffer", "-b", "pier-q", "-")
	load.Stdin = strings.NewReader(sanitize(text))
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %s", bytes.TrimSpace(out))
	}
	if out, err := exec.Command("tmux", "paste-buffer", "-p", "-d", "-b", "pier-q", "-t", pane).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %s", bytes.TrimSpace(out))
	}
	time.Sleep(150 * time.Millisecond)
	return exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run()
}

func paneAlive(pane string) bool {
	return pane != "" && exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_id}").Run() == nil
}

// deliver types the head of the queue into the primary agent when it can
// take it: ready (or, for "now" prompts, anything but asking for
// permission), and for a create's first prompt, once setup has finished.
func (a *agents) deliver(home string, setupRunning bool) {
	q := queued(home)
	if len(q) == 0 {
		return
	}
	p := q[0]
	ag := a.primary()
	if ag == nil || !paneAlive(ag.Pane) {
		return
	}
	switch {
	case ag.Pending != nil, ag.State == agentStarting:
		return
	case p.First && setupRunning:
		return
	case ag.State != agentReady && p.Mode != "now":
		return
	}
	if err := typePrompt(ag.Pane, p.Text); err != nil {
		fmt.Println("pier-supervisor: delivering a prompt:", err)
		return
	}
	markSent(home, p)
	a.mu.Lock()
	if s := a.byPane[ag.Pane]; s != nil {
		if s.State != agentWorking {
			s.Since = time.Now()
		}
		s.State, s.Waiting, s.Activity = agentWorking, "", time.Now()
	}
	a.mu.Unlock()
	a.changed()
}

// interrupt stops the primary agent's turn, as Esc does at its prompt.
func (a *agents) interrupt() error {
	ag := a.primary()
	if ag == nil {
		return errors.New("no agent running")
	}
	return exec.Command("tmux", "send-keys", "-t", ag.Pane, "Escape").Run()
}

// registerAgentOps wires the agent tracker into the supervisor's ops.
func registerAgentOps(a *agents, h *hub, home string, touch func()) {
	ops["hook"] = func(raw json.RawMessage) (any, error) {
		var r struct {
			Kind, Event, Pane string
			Payload           map[string]any
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		a.hook(r.Kind, r.Event, r.Pane, r.Payload)
		return nil, nil
	}
	ops["permission"] = func(raw json.RawMessage) (any, error) {
		var r struct {
			Pane    string
			Payload map[string]any
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		tool, _ := r.Payload["tool_name"].(string)
		input, _ := json.Marshal(r.Payload["tool_input"])
		a.hook("claude", "PermissionRequest", r.Pane, r.Payload)
		return a.requestPermission(r.Pane, tool, input, h.answerable), nil
	}
	ops["decide"] = func(raw json.RawMessage) (any, error) {
		var r struct {
			ID string
			decision
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if r.Behavior != "allow" && r.Behavior != "deny" {
			return nil, fmt.Errorf("a decision is allow or deny")
		}
		touch()
		if !a.decide(r.ID, r.decision) {
			return nil, errors.New("that request is no longer waiting — answer it in the session's terminal")
		}
		return nil, nil
	}
	ops["prompt"] = func(raw json.RawMessage) (any, error) {
		var p prompt
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, errors.New("an empty prompt")
		}
		touch()
		if err := enqueue(home, p); err != nil {
			return nil, err
		}
		a.changed()
		return map[string]bool{"queued": true}, nil
	}
	ops["interrupt"] = func(json.RawMessage) (any, error) {
		touch()
		return nil, a.interrupt()
	}
	ops["touch"] = func(json.RawMessage) (any, error) {
		touch()
		return nil, nil
	}
	h.onLastApprover = a.releaseAll
}

// agentTopic is what the "agent" stream topic carries: every agent, and
// the prompts waiting to be typed in.
type agentTopic struct {
	Agents []agentState `json:"agents"`
	Queue  []prompt     `json:"queue"`
}

func (a *agents) topic() agentTopic {
	q := queued(a.home)
	for i := range q {
		q[i].Text = truncate(q[i].Text, 2000)
	}
	return agentTopic{Agents: a.snapshot(), Queue: q}
}

// agentSummary is the primary agent in the status beacon: enough for a
// list view and its notifications without a stream.
type agentSummary struct {
	Kind    string    `json:"kind"`
	State   string    `json:"state"`
	Waiting string    `json:"waiting,omitempty"`
	Since   time.Time `json:"since"`
	Last    string    `json:"last,omitempty"`
	Queued  int       `json:"queued,omitempty"`
}

func (a *agents) summary(home string) *agentSummary {
	p := a.primary()
	if p == nil {
		return nil
	}
	return &agentSummary{Kind: p.Kind, State: p.State, Waiting: p.Waiting, Since: p.Since, Last: truncate(p.Last, 200), Queued: len(queued(home))}
}

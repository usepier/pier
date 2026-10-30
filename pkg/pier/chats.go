package pier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/pkg/pier/wire"
)

// A session's chats, from the laptop: start them, send them prompts,
// answer what their agents ask, change how they run. Everything goes
// through the session's supervisor, which runs the agents and holds each
// chat's prompt queue (so a prompt is delivered once, whatever happens to
// the connection that sent it).

// ChatRequest is a new chat: its agent, how it runs, its first prompt.
type ChatRequest struct {
	// ID makes the create happen once; "" picks a new one.
	ID     string
	Agent  string // claude | codex
	Model  string
	Effort string
	Fast   bool
	Mode   string // permission mode; see AgentModes
	Title  string
	Prompt *wire.ChatPrompt
}

func (r *ChatRequest) check() error {
	switch r.Agent {
	case "claude", "codex":
	default:
		return fmt.Errorf("unknown agent %q (claude or codex)", r.Agent)
	}
	if r.Mode != "" {
		ok := false
		for _, m := range AgentModes(r.Agent) {
			ok = ok || m.ID == r.Mode
		}
		if !ok {
			return fmt.Errorf("%s has no permission mode %q", r.Agent, r.Mode)
		}
	}
	if r.ID == "" {
		r.ID = newJobID()
	}
	if r.Prompt != nil && r.Prompt.ID == "" {
		r.Prompt.ID = newJobID()
	}
	return nil
}

// withDefaults fills what a new chat leaves open from the agent settings.
func (c *Client) withDefaults(r *ChatRequest) {
	if r.Agent == "" {
		r.Agent = c.conf().Agents.Agent()
	}
	model, effort, mode := c.conf().Agents.For(r.Agent)
	if r.Model == "" {
		r.Model = model
	}
	if r.Effort == "" {
		r.Effort = effort
	}
	if r.Mode == "" {
		r.Mode = mode
	}
}

// chatSpec is the first chat of a new session, as the create carries it.
func chatSpec(r *ChatRequest) (*driver.ChatSpec, error) {
	if r == nil {
		return nil, nil
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	sp := &driver.ChatSpec{ID: r.ID, Agent: r.Agent, Model: r.Model, Effort: r.Effort, Fast: r.Fast, Mode: r.Mode}
	if r.Prompt != nil && strings.TrimSpace(r.Prompt.Text) != "" {
		sp.Prompt, sp.PromptID = r.Prompt.Text, r.Prompt.ID
	}
	return sp, nil
}

// AgentModes are an agent's permission modes, most careful first.
func AgentModes(agent string) []wire.ModeInfo {
	if agent == "codex" {
		return []wire.ModeInfo{
			{ID: "readOnly", Name: "Read only", Description: "Reads and answers; asks before changing anything."},
			{ID: "default", Name: "Auto", Description: "Works in the repo and on the network; asks before anything else."},
			{ID: "plan", Name: "Plan", Description: "Plans first and asks you to approve the plan before it changes anything."},
			{ID: "bypassPermissions", Name: "Full access", Description: "Runs everything without asking."},
		}
	}
	return []wire.ModeInfo{
		{ID: "default", Name: "Ask", Description: "Asks before edits and commands it isn't sure are safe."},
		{ID: "acceptEdits", Name: "Accept edits", Description: "Edits files without asking; asks before commands."},
		{ID: "plan", Name: "Plan", Description: "Plans first and asks you to approve the plan before it changes anything."},
		{ID: "bypassPermissions", Name: "Bypass", Description: "Runs everything without asking."},
	}
}

// CreateChat starts a chat in a running session (once per r.ID).
func (c *Client) CreateChat(ctx context.Context, s Session, r ChatRequest) (string, error) {
	if s.State == driver.StateParked {
		return "", ErrParked
	}
	c.withDefaults(&r)
	if err := r.check(); err != nil {
		return "", err
	}
	args := map[string]any{"id": r.ID, "agent": r.Agent, "model": r.Model, "effort": r.Effort, "fast": r.Fast, "mode": r.Mode, "title": r.Title}
	if r.Prompt != nil {
		args["prompt"] = r.Prompt
	}
	_, err := c.OpID(ctx, s, "create-"+r.ID, "chat.create", args)
	return r.ID, err
}

// SendPrompt gives a chat a prompt. Its id makes it happen once: sending
// the same id again (a retry after a dropped connection) is a no-op. mode
// "steer" puts it into the running turn; otherwise it waits for the turn
// to end.
func (c *Client) SendPrompt(ctx context.Context, s Session, chatID string, p wire.ChatPrompt, mode string) error {
	if s.State == driver.StateParked {
		return ErrParked
	}
	if p.ID == "" {
		return errors.New("a prompt needs an id")
	}
	_, err := c.OpID(ctx, s, "send-"+p.ID, "chat.send", map[string]any{"chat": chatID, "mode": mode, "prompt": p})
	return err
}

// Dequeue takes back a prompt still waiting in a chat's queue.
func (c *Client) Dequeue(ctx context.Context, s Session, chatID, promptID string) error {
	_, err := c.Op(ctx, s, "chat.dequeue", map[string]string{"chat": chatID, "prompt": promptID})
	return err
}

// Interrupt stops a chat's turn.
func (c *Client) Interrupt(ctx context.Context, s Session, chatID string) error {
	_, err := c.Op(ctx, s, "chat.interrupt", map[string]string{"chat": chatID})
	return err
}

// Decision answers what a chat's agent asks.
type Decision struct {
	Behavior string // allow | allowSession | deny
	// Message is what the agent hears with a deny: why, or what to change
	// (feedback on a plan).
	Message string
	// Answers are a question's answers, by question id; Notes what the
	// user added to them.
	Answers map[string]string
	Notes   map[string]string
	// Mode is the permission mode an approved plan goes on in.
	Mode string
}

// Decide answers a chat's pending request.
func (c *Client) Decide(ctx context.Context, s Session, chatID, requestID string, d Decision) error {
	switch d.Behavior {
	case "allow", "allowSession", "deny":
	default:
		return errors.New(`a decision is "allow", "allowSession" or "deny"`)
	}
	args := map[string]any{"chat": chatID, "id": requestID, "behavior": d.Behavior, "message": d.Message, "mode": d.Mode}
	if len(d.Answers) > 0 {
		args["answers"] = d.Answers
	}
	if len(d.Notes) > 0 {
		args["notes"] = d.Notes
	}
	_, err := c.Op(ctx, s, "chat.decide", args)
	return err
}

// ChatConfig changes a chat as it goes on; empty fields (a nil Fast) stay.
type ChatConfig struct {
	Model, Effort, Mode, Title string
	Fast                       *bool
}

// ConfigureChat changes a chat's model, effort, mode or title.
func (c *Client) ConfigureChat(ctx context.Context, s Session, chatID string, cfg ChatConfig) error {
	fast := ""
	if cfg.Fast != nil {
		fast = map[bool]string{true: "on", false: "off"}[*cfg.Fast]
	}
	_, err := c.Op(ctx, s, "chat.configure", map[string]string{"chat": chatID, "model": cfg.Model, "effort": cfg.Effort, "fast": fast, "mode": cfg.Mode, "title": cfg.Title})
	return err
}

// CloseChat stops a chat and puts it away.
func (c *Client) CloseChat(ctx context.Context, s Session, chatID string) error {
	_, err := c.Op(ctx, s, "chat.close", map[string]string{"chat": chatID})
	if err == nil {
		os.RemoveAll(ChatMirrorDir(s.ID, chatID))
	}
	return err
}

// ChatPage is items of a chat's log before the one with id before (or the
// newest, when before is ""), from the session.
func (c *Client) ChatPage(ctx context.Context, s Session, chatID, before string, limit int) ([]wire.ChatItem, bool, error) {
	out, err := c.Op(ctx, s, "chat.page", map[string]any{"chat": chatID, "before": before, "limit": limit})
	if err != nil {
		return nil, false, err
	}
	var page struct {
		Items []wire.ChatItem `json:"items"`
		More  bool            `json:"more"`
	}
	if err := json.Unmarshal(out, &page); err != nil {
		return nil, false, err
	}
	return page.Items, page.More, nil
}

// ParseChats reads a "chats" message.
func ParseChats(data json.RawMessage) ([]wire.ChatInfo, error) {
	var t struct {
		Chats []wire.ChatInfo `json:"chats"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if t.Chats == nil {
		t.Chats = []wire.ChatInfo{}
	}
	return t.Chats, nil
}

// --- models -------------------------------------------------------------------

func modelsPath(agent string) string {
	return filepath.Join(config.Dir(), "models", filepath.Base(agent)+"-v3.json")
}

// Models is what an agent offers: asked of a running session when s is
// given (and remembered), else what this machine last heard, else what
// pier knows of.
func (c *Client) Models(ctx context.Context, s *Session, agent string) (wire.AgentModels, error) {
	out, err := c.models(ctx, s, agent)
	out.Models = namedModels(out.Models)
	if agent == "claude" {
		out.Models = withUltracode(withLong(out.Models))
	}
	model, effort := agentDefaults(agent, c.conf().Secrets.Manifest)
	out.Models = withAgentDefaults(out.Models, model, effort)
	return out, err
}

// withLong folds claude's 1M-context rows ("opus[1m]", "Opus 5.5 (1M)")
// into their model, as a variant it has: one Opus 5.5, with 1M context to
// switch on. The standard alias is the row's id; ID+"[1m]" runs the 1M one.
func withLong(ms []wire.ModelInfo) []wire.ModelInfo {
	const long = "[1m]"
	byID := map[string]int{}
	out := make([]wire.ModelInfo, 0, len(ms))
	for _, m := range ms {
		base, isLong := strings.CutSuffix(m.ID, long)
		if !isLong {
			if i, ok := byID[base]; ok {
				// The 1M row came first: it stands for this model.
				out[i].Description, out[i].Resolved = m.Description, m.Resolved
				continue
			}
			byID[base] = len(out)
			out = append(out, m)
			continue
		}
		m.Name = strings.TrimSuffix(m.Name, " (1M)")
		if i, ok := byID[base]; ok {
			out[i].Long = true
			out[i].Fast = out[i].Fast || m.Fast
			if m.Default {
				out[i].Default, out[i].LongDefault = true, true
			}
			continue
		}
		m.ID, m.Long, m.LongDefault = base, true, m.Default
		m.Resolved = strings.TrimSuffix(m.Resolved, long)
		byID[base] = len(out)
		out = append(out, m)
	}
	if def := slices.IndexFunc(out, func(m wire.ModelInfo) bool { return m.Default }); def >= 0 {
		for i := range out {
			out[i].Default = i == def
		}
	}
	return out
}

// withUltracode adds claude's top level — xhigh effort with dynamic
// workflows — to each model that reaches xhigh: claude's own list of
// efforts leaves it out, as it's switched on in a running chat.
func withUltracode(ms []wire.ModelInfo) []wire.ModelInfo {
	for i, m := range ms {
		if slices.Contains(m.Efforts, "xhigh") && !slices.Contains(m.Efforts, "ultracode") {
			ms[i].Efforts = append(append([]string{}, m.Efforts...), "ultracode")
		}
	}
	return ms
}

// namedModels gives each model the name people know it by — its version
// included ("Opus 5.5", which claude's list keeps in the description) —
// and folds the agent's "default" entry into the model it stands for,
// marked as the default.
func namedModels(ms []wire.ModelInfo) []wire.ModelInfo {
	var def *wire.ModelInfo
	out := make([]wire.ModelInfo, 0, len(ms))
	for _, m := range ms {
		if name, rest, ok := strings.Cut(m.Description, " · "); ok {
			m.Name = strings.Replace(name, " with 1M context", " (1M)", 1)
			m.Description = rest
		}
		if m.ID == "default" {
			d := m
			def = &d
			continue
		}
		m.Default = false
		out = append(out, m)
	}
	if def == nil {
		// No "default" entry: the agent marks its default itself.
		for i, m := range ms {
			if i < len(out) {
				out[i].Default = m.Default
			}
		}
		return out
	}
	for i := range out {
		same := out[i].Resolved != "" && out[i].Resolved == def.Resolved || def.Resolved == "" && out[i].Name == def.Name
		if same {
			out[i].Default = true
			return out
		}
	}
	// The default stands for a model the list doesn't otherwise have.
	def.Default = true
	return append([]wire.ModelInfo{*def}, out...)
}

// modelsFresh is how long a list heard from the agent's CLI on this
// machine is taken as it is; after, it's asked again. A session's list
// (what sessions actually run) stands until a session says otherwise.
const modelsFresh = 6 * time.Hour

func (c *Client) models(ctx context.Context, s *Session, agent string) (wire.AgentModels, error) {
	out := wire.AgentModels{Agent: agent, Modes: AgentModes(agent)}
	if s != nil && s.State != driver.StateParked {
		raw, err := c.Op(ctx, *s, "models", map[string]string{"agent": agent})
		var r struct {
			Models   []wire.ModelInfo    `json:"models"`
			Commands []wire.AgentCommand `json:"commands"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err == nil && len(r.Models) > 0 {
			out.Models, out.Commands = r.Models, r.Commands
			c.keepModels(agent, "session", r.Models, r.Commands)
			return out, nil
		}
		if err != nil {
			c.logf("%s's models from %s: %v — using the last known", agent, s.Name, err)
		}
	}
	kept, from, at := keptModels(agent)
	if len(kept.Models) > 0 && (from == "session" || time.Since(at) < modelsFresh) {
		out.Models, out.Commands = kept.Models, kept.Commands
		return out, nil
	}
	// Before any session has said: the agent's CLI here.
	if ms, cs, err := localModels(ctx, agent); err == nil && len(ms) > 0 {
		out.Models, out.Commands = ms, cs
		c.keepModels(agent, "here", ms, cs)
		return out, nil
	} else if err != nil {
		c.logf("%s's models here: %v", agent, err)
	}
	if len(kept.Models) > 0 {
		out.Models, out.Commands = kept.Models, kept.Commands
		return out, nil
	}
	out.Models = knownModels(agent)
	return out, nil
}

// keptList is a model list as kept: who said it (a session, or the CLI
// here) and what.
type keptList struct {
	From     string              `json:"from"`
	Models   []wire.ModelInfo    `json:"models"`
	Commands []wire.AgentCommand `json:"commands,omitempty"`
}

func (c *Client) keepModels(agent, from string, ms []wire.ModelInfo, cs []wire.AgentCommand) {
	b, _ := json.Marshal(keptList{From: from, Models: ms, Commands: cs})
	err := os.MkdirAll(filepath.Dir(modelsPath(agent)), 0o700)
	if err == nil {
		err = writeFileAtomic(modelsPath(agent), b)
	}
	if err != nil {
		c.logf("keeping %s's models: %v", agent, err)
	}
	// Lists kept before they said which models have a fast mode, or which
	// commands the agent has.
	for _, old := range []string{".json", "-v2.json"} {
		_ = os.Remove(filepath.Join(config.Dir(), "models", filepath.Base(agent)+old))
	}
}

// keptModels is the list last heard, who said it, and when.
func keptModels(agent string) (keptList, string, time.Time) {
	fi, err := os.Stat(modelsPath(agent))
	if err != nil {
		return keptList{}, "", time.Time{}
	}
	b, err := os.ReadFile(modelsPath(agent))
	if err != nil {
		return keptList{}, "", time.Time{}
	}
	var k keptList
	if json.Unmarshal(b, &k) != nil {
		return keptList{}, "", time.Time{}
	}
	return k, k.From, fi.ModTime()
}

// knownModels is what pier knows an agent offers before any session said.
func knownModels(agent string) []wire.ModelInfo {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	if agent == "codex" {
		return []wire.ModelInfo{{ID: "", Name: "Default", Description: "The model your codex account defaults to.", Default: true}}
	}
	return []wire.ModelInfo{
		{ID: "opus", Name: "Opus", Description: "For complex work.", Efforts: efforts, Default: true},
		{ID: "sonnet", Name: "Sonnet", Description: "Efficient for most tasks.", Efforts: efforts},
		{ID: "haiku", Name: "Haiku", Description: "Fastest for quick answers."},
	}
}

// --- delivering to a parked session ---------------------------------------------

// Delivery is a prompt for a chat of a session that may be parked: the
// background job wakes it, starts the chat if it is new (Chat.Agent set),
// uploads the files, and sends the prompt.
type Delivery struct {
	Chat   ChatRequest
	Prompt wire.ChatPrompt
	// Files are files on this machine that go with the prompt.
	Files []string
	Mode  string
}

// StartDeliver sends a prompt to a parked session in the background: the
// job finishes even if the frontend that asked quits.
func (c *Client) StartDeliver(s Session, d Delivery) (Job, error) {
	if d.Prompt.ID == "" {
		return Job{}, errors.New("a prompt needs an id")
	}
	dir := jobsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Job{}, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return Job{}, err
	}
	// The prompt rides a private file, not argv: process lists are
	// readable by every user of the machine.
	f := filepath.Join(dir, "deliver-"+d.Prompt.ID+".json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		return Job{}, err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(config.Dir(), "logs", "deliver-"+sanitizeStem(s.Name)+".log")
	return startJob(JobDeliver, s.Repo, s.Name, home, logPath, false, "deliver", s.Name, "--file", f)
}

// ReadDelivery reads a delivery StartDeliver wrote.
func ReadDelivery(path string) (Delivery, error) {
	var d Delivery
	b, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(b, &d)
}

// Deliver is the background job StartDeliver runs: wake the session if it
// is parked, then start the chat and send the prompt as soon as its
// supervisor answers.
func (c *Client) Deliver(ctx context.Context, s Session, d Delivery, progress Progress) error {
	start := time.Now()
	if s.State == driver.StateParked {
		progress.emit(start, EventStep, "waking "+s.Name)
		if err := c.Resume(ctx, s); err != nil {
			return err
		}
		s.State = driver.StateRunning
	}
	for _, f := range d.Files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		progress.emit(start, EventStep, "uploading "+filepath.Base(f))
		path, err := c.Upload(ctx, s, filepath.Base(f), data)
		if err != nil {
			return err
		}
		d.Prompt.Attachments = append(d.Prompt.Attachments, wire.Attachment{Name: filepath.Base(f), Path: path, Mime: mimeOf(f)})
	}
	progress.emit(start, EventStep, "sending the prompt")
	// The supervisor's socket comes up a moment after the session answers
	// ssh: keep asking for a minute.
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		if d.Chat.Agent != "" {
			r := d.Chat
			r.Prompt = &d.Prompt
			_, err = c.CreateChat(ctx, s, r)
		} else {
			err = c.SendPrompt(ctx, s, d.Chat.ID, d.Prompt, d.Mode)
		}
		if err == nil {
			for _, f := range d.Files {
				os.Remove(f)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return err
}

// ChatWindow opens the session's chats in its tmux: the read-only live
// view of its chats (made if missing, selected), or — takeover — a window
// continuing chatID in the terminal. It returns the tmux window id.
func (c *Client) ChatWindow(ctx context.Context, s Session, chatID string, takeover bool) (string, error) {
	if err := CheckReady(s); err != nil {
		return "", err
	}
	// An older supervisor has no chats (and would take the command for a
	// start of its loop): asking its version brings it up to this pier's.
	if _, err := c.Op(ctx, s, "version", nil); err != nil {
		return "", err
	}
	script := "pier-supervisor chat window"
	if takeover {
		script += " --takeover"
	}
	if chatID != "" {
		script += " " + shellQuote(chatID)
	}
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

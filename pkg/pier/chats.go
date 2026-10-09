package pier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// chatSpec is the first chat of a new session, as the create carries it.
func chatSpec(r *ChatRequest) (*driver.ChatSpec, error) {
	if r == nil {
		return nil, nil
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	sp := &driver.ChatSpec{ID: r.ID, Agent: r.Agent, Model: r.Model, Effort: r.Effort, Mode: r.Mode}
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
	if err := r.check(); err != nil {
		return "", err
	}
	args := map[string]any{"id": r.ID, "agent": r.Agent, "model": r.Model, "effort": r.Effort, "mode": r.Mode, "title": r.Title}
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

// ChatConfig changes a chat as it goes on; empty fields stay.
type ChatConfig struct {
	Model, Effort, Mode, Title string
}

// ConfigureChat changes a chat's model, effort, mode or title.
func (c *Client) ConfigureChat(ctx context.Context, s Session, chatID string, cfg ChatConfig) error {
	_, err := c.Op(ctx, s, "chat.configure", map[string]string{"chat": chatID, "model": cfg.Model, "effort": cfg.Effort, "mode": cfg.Mode, "title": cfg.Title})
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
	return filepath.Join(config.Dir(), "models", filepath.Base(agent)+".json")
}

// Models is what an agent offers: asked of a running session when s is
// given (and remembered), else what this machine last heard, else what
// pier knows of.
func (c *Client) Models(ctx context.Context, s *Session, agent string) (wire.AgentModels, error) {
	out := wire.AgentModels{Agent: agent, Modes: AgentModes(agent)}
	if s != nil && s.State != driver.StateParked {
		raw, err := c.Op(ctx, *s, "models", map[string]string{"agent": agent})
		if err == nil {
			var r struct {
				Models []wire.ModelInfo `json:"models"`
			}
			if json.Unmarshal(raw, &r) == nil && len(r.Models) > 0 {
				out.Models = r.Models
				if b, err := json.Marshal(r.Models); err == nil {
					_ = os.MkdirAll(filepath.Dir(modelsPath(agent)), 0o700)
					_ = writeFileAtomic(modelsPath(agent), b)
				}
				return out, nil
			}
		}
	}
	if b, err := os.ReadFile(modelsPath(agent)); err == nil {
		if json.Unmarshal(b, &out.Models) == nil && len(out.Models) > 0 {
			return out, nil
		}
	}
	out.Models = knownModels(agent)
	return out, nil
}

// knownModels is what pier knows an agent offers before any session said.
func knownModels(agent string) []wire.ModelInfo {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	if agent == "codex" {
		return []wire.ModelInfo{{ID: "", Name: "Default", Description: "The model your codex account defaults to.", Default: true}}
	}
	return []wire.ModelInfo{
		{ID: "default", Name: "Default", Description: "The model your account defaults to.", Efforts: efforts, Default: true},
		{ID: "opus", Name: "Opus", Description: "For complex work.", Efforts: efforts},
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

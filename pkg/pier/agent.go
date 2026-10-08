package pier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
)

// A session's agent, from the laptop: send it prompts, interrupt it, answer
// its permission requests. Everything goes through the session's
// supervisor, which owns the prompt queue (so a prompt is delivered once,
// whatever happens to the connection that sent it) and the pending
// requests.

// AgentStatus is the session's agent as its supervisor last reported it.
type AgentStatus = driver.AgentStatus

// SendPrompt queues a prompt for a running session's agent. id makes it
// happen once: sending the same id again (a retry after a dropped
// connection) is a no-op. mode "now" types it in at once, even mid-turn;
// otherwise it waits until the agent is free.
func (c *Client) SendPrompt(ctx context.Context, s Session, id, text, mode string) error {
	if s.State == driver.StateParked {
		return ErrParked
	}
	_, err := c.Op(ctx, s, "prompt", map[string]string{"id": id, "text": text, "mode": mode})
	return err
}

// StartDeliver sends a prompt to a parked session in the background: the
// job wakes it and queues the prompt there, and finishes even if the
// frontend that asked quits.
func (c *Client) StartDeliver(s Session, id, text, mode string) (Job, error) {
	dir := jobsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Job{}, err
	}
	pf := filepath.Join(dir, "prompt-"+id)
	if err := os.WriteFile(pf, []byte(text), 0o600); err != nil {
		return Job{}, err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(config.Dir(), "logs", "deliver-"+sanitizeStem(s.Name)+".log")
	return startJob(JobDeliver, s.Repo, s.Name, home, logPath, false,
		"deliver", s.Name, "--id", id, "--prompt-file", pf, "--mode", mode)
}

// Deliver is the background job StartDeliver runs: wake the session if it
// is parked, then queue the prompt as soon as its supervisor answers.
func (c *Client) Deliver(ctx context.Context, s Session, id, text, mode string, progress Progress) error {
	start := time.Now()
	if s.State == driver.StateParked {
		progress.emit(start, EventStep, "waking "+s.Name)
		if err := c.Resume(ctx, s); err != nil {
			return err
		}
		s.State = driver.StateRunning
	}
	progress.emit(start, EventStep, "queueing the prompt")
	// The supervisor's socket comes up a moment after the session answers
	// ssh: keep asking for a minute.
	var err error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		if err = c.SendPrompt(ctx, s, id, text, mode); err == nil {
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

// Interrupt stops the agent's turn, as Esc does at its prompt.
func (c *Client) Interrupt(ctx context.Context, s Session) error {
	_, err := c.Op(ctx, s, "interrupt", nil)
	return err
}

// Decide answers a pending permission request: behavior "allow" or "deny"
// (with message, the reason the agent hears).
func (c *Client) Decide(ctx context.Context, s Session, requestID, behavior, message string, updatedInput json.RawMessage) error {
	if behavior != "allow" && behavior != "deny" {
		return errors.New(`a decision is "allow" or "deny"`)
	}
	args := map[string]any{"id": requestID, "behavior": behavior, "message": message}
	if len(updatedInput) > 0 {
		args["updated_input"] = updatedInput
	}
	_, err := c.Op(ctx, s, "decide", args)
	return err
}

// AgentDetail is the session's agent as its stream reports it ("agent").
type AgentDetail struct {
	AgentStatus
	Model string
	// Pending is the permission request waiting on a frontend.
	Pending *PermissionRequest
	// Queue is the prompts waiting to be typed in, oldest first.
	Queue []QueuedPrompt
}

// PermissionRequest is the agent asking to use a tool.
type PermissionRequest struct {
	ID    string
	Tool  string
	Input json.RawMessage
	At    time.Time
}

// QueuedPrompt is a prompt the session holds for its agent.
type QueuedPrompt struct {
	ID   string
	Text string
	At   time.Time
}

// ParseAgent reads an "agent" message: the agent prompts go to (or, with
// none running, the last one that ran). nil when no agent ever ran.
func ParseAgent(data json.RawMessage) (*AgentDetail, error) {
	var raw struct {
		Agents []struct {
			Kind    string    `json:"kind"`
			State   string    `json:"state"`
			Waiting string    `json:"waiting"`
			Model   string    `json:"model"`
			Since   time.Time `json:"since"`
			Last    string    `json:"last"`
			Pending *struct {
				ID    string          `json:"id"`
				Tool  string          `json:"tool"`
				Input json.RawMessage `json:"input"`
				At    time.Time       `json:"at"`
			} `json:"pending"`
		} `json:"agents"`
		Queue []struct {
			ID   string    `json:"id"`
			Text string    `json:"text"`
			At   time.Time `json:"at"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	pick := -1
	for i, a := range raw.Agents {
		switch {
		case pick < 0:
			pick = i
		case (a.State != "ended") != (raw.Agents[pick].State != "ended"):
			if a.State != "ended" {
				pick = i
			}
		case a.Since.After(raw.Agents[pick].Since):
			pick = i
		}
	}
	if pick < 0 {
		return nil, nil
	}
	a := raw.Agents[pick]
	d := &AgentDetail{AgentStatus: AgentStatus{Kind: a.Kind, State: a.State, Waiting: a.Waiting, Since: a.Since, Last: a.Last, Queued: len(raw.Queue)}, Model: a.Model}
	if p := a.Pending; p != nil {
		d.Pending = &PermissionRequest{ID: p.ID, Tool: p.Tool, Input: p.Input, At: p.At}
	}
	for _, q := range raw.Queue {
		d.Queue = append(d.Queue, QueuedPrompt{ID: q.ID, Text: q.Text, At: q.At})
	}
	return d, nil
}

// PeekAgent is the bottom of the agent's pane as text: a look at what it is
// doing without attaching (which would keep the session awake).
func (c *Client) PeekAgent(ctx context.Context, s Session, lines int) (string, error) {
	if err := CheckReady(s); err != nil {
		return "", err
	}
	if lines <= 0 || lines > 200 {
		lines = 40
	}
	script := `w=$(tmux list-windows -t main -F '#{window_id} #{@pier-role}' 2>/dev/null | awk '$2=="agent"{print $1; exit}')
[ -n "$w" ] && tmux capture-pane -p -J -t "$w" -S -` + strconv.Itoa(lines)
	out, err := c.exec(ctx, s.ID, script)
	return strings.TrimRight(out, "\n "), err
}

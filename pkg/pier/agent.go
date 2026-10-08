package pier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

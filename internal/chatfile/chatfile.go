// Package chatfile is the on-disk form of a session's chats
// (~/.pier/chats/<id>), shared by the supervisor that runs them and the
// create that seeds a session's first.
package chatfile

import (
	"path/filepath"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// Root is where chats live, under the session user's home.
const Root = ".pier/chats"

// Record is a chat's chat.json.
type Record struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"` // claude | codex
	Title  string `json:"title,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
	// Conversation is the agent's own id for it: claude's session id
	// (chosen up front), codex's thread id (once it starts one).
	Conversation string `json:"conversation,omitempty"`
	// Started: the agent has the conversation on disk (claude resumes it).
	Started bool      `json:"started,omitempty"`
	Created time.Time `json:"created"`
	Log     string    `json:"log,omitempty"`
	// State and Waiting as last recorded: a chat found working after a
	// restart was cut off.
	State   wire.AgentState `json:"state,omitempty"`
	Waiting string          `json:"waiting,omitempty"`
	Last    string          `json:"last,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Prompt is a prompt for a chat: queue/<id>.json until delivered, then
// sent/<id>.json.
type Prompt struct {
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Attachments []wire.Attachment `json:"attachments,omitempty"`
	At          time.Time         `json:"at"`
	// First marks a create's first prompt: it waits for setup to finish.
	First bool `json:"first,omitempty"`
}

// Dir is a chat's directory, relative to the home it lives in.
func Dir(id string) string { return filepath.Join(Root, id) }

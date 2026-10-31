package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

// Event is what one message of an agent's stream means to its chat: items
// added or changed, text streaming into the items being written, and what
// the agent's host needs to know.
type Event struct {
	Changes []Change
	Deltas  []wire.ChatDelta
	// Session is the conversation's id, once the agent says it; Model the
	// model it runs.
	Session string
	Model   string
	// TurnEnd is set when a turn ends.
	TurnEnd *TurnEnd
}

// TurnEnd is how a turn ended.
type TurnEnd struct {
	// Error says why it failed; empty when it finished or was interrupted.
	Error       string
	Interrupted bool
}

// ClaudeStream reads claude's headless stream (--output-format
// stream-json --include-partial-messages): the conversation through the
// transcript reader (the stream's messages share the transcript's shape),
// the text being written from its partial messages.
type ClaudeStream struct {
	p *claude
	// message is the id of the message streaming in.
	message string
	// interrupted: this turn was interrupted (its end is no failure).
	interrupted bool
	prefix      string
	n           int
}

// NewClaudeStream starts reading a claude stream. prefix goes before the
// ids of the items it makes up (statuses, errors): unique per reading, so
// they never collide with an earlier reading's in the same chat.
func NewClaudeStream(prefix string) *ClaudeStream {
	return &ClaudeStream{p: &claude{tools: map[string]*wire.ChatItem{}}, prefix: prefix}
}

type claudeStreamLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Session string          `json:"session_id"`
	Model   string          `json:"model"`
	Parent  *string         `json:"parent_tool_use_id"`
	Event   json.RawMessage `json:"event"`
	// result
	IsError bool     `json:"is_error"`
	Result  string   `json:"result"`
	Errors  []string `json:"errors"`
	// api_retry
	Attempt    int `json:"attempt"`
	MaxRetries int `json:"max_retries"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// Feed reads one line of the stream.
func (s *ClaudeStream) Feed(line []byte) Event {
	var l claudeStreamLine
	if err := json.Unmarshal(line, &l); err != nil {
		s.p.bad++
		return Event{}
	}
	switch l.Type {
	case "stream_event":
		if l.Parent != nil && *l.Parent != "" {
			return Event{} // a subagent's text shows when it is done
		}
		var e streamEvent
		if json.Unmarshal(l.Event, &e) != nil {
			return Event{}
		}
		switch e.Type {
		case "message_start":
			s.message = e.Message.ID
		case "content_block_delta":
			id := fmt.Sprintf("%s:%d", s.message, e.Index)
			switch {
			case e.Delta.Type == "text_delta" && e.Delta.Text != "":
				return Event{Deltas: []wire.ChatDelta{{ID: id, Kind: wire.ChatAssistant, Text: e.Delta.Text}}}
			case e.Delta.Type == "thinking_delta" && e.Delta.Thinking != "":
				return Event{Deltas: []wire.ChatDelta{{ID: id, Kind: wire.ChatThinking, Text: e.Delta.Thinking}}}
			}
		}
		return Event{}
	case "system":
		switch l.Subtype {
		case "init":
			return Event{Session: l.Session, Model: l.Model}
		case "api_retry":
			if l.Attempt == 1 {
				return Event{Changes: []Change{s.status(fmt.Sprintf("retrying a failed request (up to %d times)", l.MaxRetries))}}
			}
			return Event{}
		}
	case "result":
		end := &TurnEnd{Interrupted: s.interrupted}
		if l.Subtype != "success" && !s.interrupted {
			end.Error = resultError(l)
		}
		s.interrupted = false
		ev := Event{TurnEnd: end}
		// Whatever still runs when the turn is over was cut off.
		for _, it := range s.p.tools {
			if it.Tool.Status == wire.ToolRunning {
				it.Tool.Status = wire.ToolInterrupted
				ev.Changes = append(ev.Changes, Change{Op: wire.ChatUpdate, Item: *it})
			}
		}
		if end.Error != "" {
			ev.Changes = append(ev.Changes, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: s.nextID("e"), Kind: wire.ChatError, Time: now(), Text: end.Error}})
		}
		return ev
	case "user", "assistant":
		cs := s.p.Feed(line)
		for _, c := range cs {
			if c.Item.Kind == wire.ChatStatus && c.Item.Text == "interrupted" {
				s.interrupted = true
			}
		}
		return Event{Changes: cs}
	}
	return Event{}
}

func resultError(l claudeStreamLine) string {
	switch l.Subtype {
	case "error_max_turns":
		return "The turn stopped: it reached its limit of steps."
	case "error_max_budget_usd":
		return "The turn stopped: it reached its budget."
	}
	if len(l.Errors) > 0 {
		return strings.Join(l.Errors, "\n")
	}
	if l.Result != "" {
		return l.Result
	}
	return "The turn stopped with an error (" + l.Subtype + ")."
}

func (s *ClaudeStream) nextID(prefix string) string {
	s.n++
	return fmt.Sprintf("%s%s%d", s.prefix, prefix, s.n)
}

func (s *ClaudeStream) status(text string) Change {
	return Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: s.nextID("s"), Kind: wire.ChatStatus, Time: now(), Text: text}}
}

// Bad counts lines it couldn't read.
func (s *ClaudeStream) Bad() int { return s.p.bad }

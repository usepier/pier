package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

// codex reads Codex's rollout files
// (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl): {timestamp, type,
// payload} lines. The conversation reads best from its event messages
// (the prompts and replies as shown), tool calls from its response items.
type codex struct {
	tools   map[string]*wire.ChatItem
	n       int
	prefix  string
	partial bool
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	Message   string          `json:"message"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
}

func (c *codex) Partial() bool { return c.partial }

func (c *codex) id(prefix string) string {
	c.n++
	return fmt.Sprintf("%s%s%d", c.prefix, prefix, c.n)
}

func (c *codex) Feed(line []byte) []Change {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		c.partial = true
		return nil
	}
	var p codexPayload
	if json.Unmarshal(l.Payload, &p) != nil {
		return nil
	}
	at := parseTime(l.Timestamp)
	switch l.Type {
	case "event_msg":
		switch p.Type {
		case "user_message":
			t := strings.TrimSpace(p.Message)
			if t == "" || strings.HasPrefix(t, "<environment_context>") || strings.HasPrefix(t, "<user_instructions>") {
				return nil
			}
			t, _ = clip(t, maxText)
			return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.id("u"), Kind: wire.ChatUser, Time: at, Text: t}}}
		case "agent_message":
			if strings.TrimSpace(p.Message) == "" {
				return nil
			}
			t, _ := clip(p.Message, maxText)
			return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.id("a"), Kind: wire.ChatAssistant, Time: at, Text: t}}}
		case "agent_reasoning":
			if strings.TrimSpace(p.Text) == "" {
				return nil
			}
			t, _ := clip(p.Text, maxText)
			return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.id("r"), Kind: wire.ChatThinking, Time: at, Text: t}}}
		case "turn_aborted":
			var out []Change
			for _, it := range c.tools {
				if it.Tool.Status == wire.ToolRunning {
					it.Tool.Status = wire.ToolInterrupted
					out = append(out, Change{Op: wire.ChatUpdate, Item: *it})
				}
			}
			return append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.id("s"), Kind: wire.ChatStatus, Time: at, Text: "interrupted"}})
		case "error":
			return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.id("e"), Kind: wire.ChatError, Time: at, Text: p.Message}}}
		}
	case "response_item":
		switch p.Type {
		case "function_call":
			it := &wire.ChatItem{ID: "call:" + p.CallID, Kind: wire.ChatTool, Time: at, Tool: codexFunction(p)}
			c.tools[p.CallID] = it
			return []Change{{Op: wire.ChatAppend, Item: *it}}
		case "custom_tool_call":
			it := &wire.ChatItem{ID: "call:" + p.CallID, Kind: wire.ChatTool, Time: at, Tool: codexCustom(p)}
			c.tools[p.CallID] = it
			return []Change{{Op: wire.ChatAppend, Item: *it}}
		case "function_call_output", "custom_tool_call_output":
			it := c.tools[p.CallID]
			if it == nil {
				return nil
			}
			codexResult(it.Tool, p.Output)
			return []Change{{Op: wire.ChatUpdate, Item: *it}}
		}
	}
	return nil
}

func codexFunction(p codexPayload) *wire.ToolCall {
	args := inputMap(json.RawMessage(p.Arguments))
	t := &wire.ToolCall{ID: p.CallID, Name: p.Name, Status: wire.ToolRunning, View: wire.ToolGeneric, Input: jsonString(args)}
	switch p.Name {
	case "shell", "exec_command", "local_shell":
		cmd := shellCommand(args["command"])
		if cmd == "" {
			cmd = str(args, "cmd")
		}
		t.View, t.Summary = wire.ToolBash, oneLine(cmd)
		t.Bash = &wire.BashView{Command: cmd}
	case "update_plan":
		t.View, t.Summary = wire.ToolTodo, "updated the plan"
		plan, _ := args["plan"].([]any)
		for _, x := range plan {
			m, _ := x.(map[string]any)
			if m != nil {
				t.Todos = append(t.Todos, wire.TodoItem{Content: str(m, "step"), Status: str(m, "status")})
			}
		}
	default:
		t.Summary = p.Name
	}
	return t
}

// shellCommand reads codex's command: an argv, usually ["bash","-lc","…"].
func shellCommand(v any) string {
	list, _ := v.([]any)
	var argv []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			argv = append(argv, s)
		}
	}
	if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

func codexCustom(p codexPayload) *wire.ToolCall {
	t := &wire.ToolCall{ID: p.CallID, Name: p.Name, Status: wire.ToolRunning, View: wire.ToolGeneric}
	if p.Name == "apply_patch" {
		t.View = wire.ToolEdit
		t.Edit = applyPatchView(p.Input)
		t.Summary = t.Edit.Path
		return t
	}
	t.Input, _ = clip(p.Input, maxInput)
	t.Summary = p.Name
	return t
}

// applyPatchView reads the first file of an apply_patch envelope.
func applyPatchView(patch string) *wire.EditView {
	v := &wire.EditView{}
	var h *wire.Hunk
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File: "), strings.HasPrefix(line, "*** Add File: "):
			if v.Path != "" {
				return v // the first file is enough for the card
			}
			v.Path = strings.TrimSpace(line[strings.Index(line, ":")+1:])
			v.Created = strings.HasPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "@@"):
			v.Hunks = append(v.Hunks, wire.Hunk{})
			h = &v.Hunks[len(v.Hunks)-1]
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"), strings.HasPrefix(line, " "):
			if strings.HasPrefix(line, "***") {
				continue
			}
			if h == nil {
				v.Hunks = append(v.Hunks, wire.Hunk{})
				h = &v.Hunks[len(v.Hunks)-1]
			}
			h.Lines = append(h.Lines, line)
		}
	}
	return v
}

// codexResult completes a call from its output: plain text, or JSON with
// the output and its exit code.
func codexResult(t *wire.ToolCall, raw json.RawMessage) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	out, code := s, -1
	var structured struct {
		Output   string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(s), &structured) == nil && (structured.Output != "" || structured.Metadata.ExitCode != nil) {
		out = structured.Output
		if structured.Metadata.ExitCode != nil {
			code = *structured.Metadata.ExitCode
		}
	}
	t.Status = wire.ToolOK
	if code > 0 || strings.Contains(s, "aborted") {
		t.Status = wire.ToolFailed
	}
	if t.Bash != nil {
		t.Bash.Output, t.Truncated = clipTail(strings.TrimRight(out, "\n"), maxResult)
		if code >= 0 {
			c := code
			t.Bash.ExitCode = &c
		}
		return
	}
	t.Result, t.Truncated = clip(out, maxResult)
}

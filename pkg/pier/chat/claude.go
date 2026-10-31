package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// claude reads Claude Code's messages: its session transcripts
// (~/.claude/projects/<project>/<session>.jsonl) and the stream it writes
// running headless (stream-json), which share their shape: one JSON object
// per line, "user" and "assistant" lines carrying API-style messages, tool
// results arriving as user lines with a structured result beside them.
type claude struct {
	tools   map[string]*wire.ChatItem
	blocks  map[string]int // blocks seen per message id
	bad     int
	partial bool
}

type claudeLine struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	UUID          string          `json:"uuid"`
	Timestamp     string          `json:"timestamp"`
	IsMeta        bool            `json:"isMeta"`
	IsSidechain   bool            `json:"isSidechain"`
	Content       string          `json:"content"`
	Message       json.RawMessage `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	// The stream's spellings: the structured result, and the subagent
	// call a line belongs to.
	StreamResult json.RawMessage `json:"tool_use_result"`
	Parent       *string         `json:"parent_tool_use_id"`
}

func (l claudeLine) sidechain() bool { return l.IsSidechain || (l.Parent != nil && *l.Parent != "") }

func (l claudeLine) structured() json.RawMessage {
	if len(l.ToolUseResult) > 0 {
		return l.ToolUseResult
	}
	return l.StreamResult
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (c *claude) Partial() bool { return c.partial }

var commandName = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)

func (c *claude) Feed(line []byte) []Change {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		c.bad++
		c.partial = true
		return nil
	}
	at := parseTime(l.Timestamp)
	if at.IsZero() {
		at = now()
	}
	switch l.Type {
	case "user":
		return c.user(l, at)
	case "assistant":
		return c.assistant(l, at)
	case "system":
		if l.Subtype == "compact_boundary" {
			return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: l.UUID, Kind: wire.ChatStatus, Time: at, Text: "conversation compacted"}}}
		}
	}
	// summary, file snapshots, hook output: not part of the conversation.
	return nil
}

func (c *claude) user(l claudeLine, at time.Time) []Change {
	var m claudeMessage
	if json.Unmarshal(l.Message, &m) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return c.userText(l, at, text, 0)
	}
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		c.partial = true
		return nil
	}
	var out []Change
	var texts []string
	images := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "image":
			images++
		case "tool_result":
			if ch := c.toolResult(b, l.structured()); ch != nil {
				out = append(out, *ch)
			}
		}
	}
	if len(texts) > 0 || images > 0 {
		out = append(out, c.userText(l, at, strings.Join(texts, "\n"), images)...)
	}
	return out
}

func (c *claude) userText(l claudeLine, at time.Time, text string, images int) []Change {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "[Request interrupted by user"):
		var out []Change
		for _, it := range c.tools {
			if it.Tool.Status == wire.ToolRunning {
				it.Tool.Status = wire.ToolInterrupted
				out = append(out, Change{Op: wire.ChatUpdate, Item: *it})
			}
		}
		return append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: l.UUID, Kind: wire.ChatStatus, Time: at, Text: "interrupted"}})
	case strings.Contains(t, "<command-name>"):
		name := ""
		if m := commandName.FindStringSubmatch(t); m != nil {
			name = strings.TrimSpace(m[1])
		}
		if name == "" {
			return nil
		}
		return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: l.UUID, Kind: wire.ChatStatus, Time: at, Text: "ran " + name}}}
	case l.IsMeta, strings.HasPrefix(t, "<local-command-"), strings.HasPrefix(t, "<system-reminder>"):
		return nil
	}
	if t == "" && images == 0 {
		return nil
	}
	t, _ = clip(t, maxText)
	var files []wire.Attachment
	for range images {
		files = append(files, wire.Attachment{Name: "image", Mime: "image/*"})
	}
	return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: l.UUID, Kind: wire.ChatUser, Time: at, Text: t, Attachments: files, Sidechain: l.sidechain()}}}
}

func (c *claude) assistant(l claudeLine, at time.Time) []Change {
	var m claudeMessage
	if json.Unmarshal(l.Message, &m) != nil {
		return nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		c.partial = true
		return nil
	}
	var out []Change
	side := l.sidechain()
	for i, b := range blocks {
		id := c.blockID(l, m, i)
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			t, _ := clip(b.Text, maxText)
			if strings.HasPrefix(t, "API Error") {
				out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: id, Kind: wire.ChatError, Time: at, Text: t}})
				continue
			}
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: id, Kind: wire.ChatAssistant, Time: at, Text: t, Model: m.Model, Sidechain: side}})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			t, _ := clip(b.Thinking, maxText)
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: id, Kind: wire.ChatThinking, Time: at, Text: t, Sidechain: side}})
		case "tool_use":
			it := &wire.ChatItem{ID: b.ID, Kind: wire.ChatTool, Time: at, Sidechain: side, Tool: claudeTool(b)}
			c.tools[b.ID] = it
			out = append(out, Change{Op: wire.ChatAppend, Item: *it})
		}
	}
	return out
}

// blockID names a message block: by the message's id and the block's place
// in it, the names its streamed deltas carry. A message's blocks arrive one
// line each, so the place counts across lines.
func (c *claude) blockID(l claudeLine, m claudeMessage, i int) string {
	if m.ID == "" {
		return fmt.Sprintf("%s:%d", l.UUID, i)
	}
	if c.blocks == nil {
		c.blocks = map[string]int{}
	}
	n := c.blocks[m.ID]
	c.blocks[m.ID] = n + 1
	return fmt.Sprintf("%s:%d", m.ID, n)
}

func inputMap(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// claudeTool describes a tool call from its name and input.
func claudeTool(b claudeBlock) *wire.ToolCall {
	in := inputMap(b.Input)
	t := &wire.ToolCall{ID: b.ID, Name: b.Name, Status: wire.ToolRunning, View: wire.ToolGeneric, Input: jsonString(in)}
	switch b.Name {
	case "Bash":
		t.View = wire.ToolBash
		t.Summary = oneLine(str(in, "command"))
		t.Bash = &wire.BashView{Command: str(in, "command"), Description: str(in, "description")}
	case "Edit", "MultiEdit", "NotebookEdit":
		t.View = wire.ToolEdit
		p := str(in, "file_path")
		if p == "" {
			p = str(in, "notebook_path")
		}
		t.Summary = p
		t.Edit = &wire.EditView{Path: p, Hunks: proposedHunks(in)}
	case "Write":
		t.View = wire.ToolWrite
		t.Summary = str(in, "file_path")
		t.Edit = &wire.EditView{Path: t.Summary, Created: true}
		if content := str(in, "content"); content != "" {
			t.Edit.Hunks = []wire.Hunk{newFileHunk(content)}
		}
	case "Read":
		t.View = wire.ToolRead
		t.Summary = str(in, "file_path")
	case "Grep", "Glob":
		t.View = wire.ToolSearch
		t.Summary = str(in, "pattern")
		if p := str(in, "path"); p != "" {
			t.Summary += " in " + p
		}
	case "TodoWrite":
		t.View = wire.ToolTodo
		t.Summary = "updated the task list"
		t.Todos = todos(in["todos"])
	case "WebFetch":
		t.View = wire.ToolWeb
		t.Summary = str(in, "url")
	case "WebSearch":
		t.View = wire.ToolWeb
		t.Summary = str(in, "query")
	case "Task", "Agent":
		t.View = wire.ToolTask
		t.Summary = oneLine(str(in, "description"))
	case "AskUserQuestion":
		t.View = wire.ToolAsk
		_, t.Questions, _ = Asks(b.Name, b.Input)
		if len(t.Questions) > 0 {
			t.Summary = oneLine(t.Questions[0].Question)
		}
	case "ExitPlanMode":
		t.View = wire.ToolPlan
		_, _, t.Plan = Asks(b.Name, b.Input)
		t.Summary = firstLine(t.Plan)
	default:
		t.Summary = b.Name
	}
	return t
}

// ClaudeCall describes a claude tool call from its name and input, as the
// chat shows it before it runs: an edit's proposed change included.
func ClaudeCall(id, name string, input json.RawMessage) *wire.ToolCall {
	return claudeTool(claudeBlock{ID: id, Name: name, Input: input})
}

// proposedHunks is an edit as asked: the old text out, the new text in
// (line numbers unknown until it applies).
func proposedHunks(in map[string]any) []wire.Hunk {
	type edit struct{ old, new string }
	var edits []edit
	if o, n := str(in, "old_string"), str(in, "new_string"); o != "" || n != "" {
		edits = append(edits, edit{o, n})
	}
	list, _ := in["edits"].([]any)
	for _, x := range list {
		if m, _ := x.(map[string]any); m != nil {
			edits = append(edits, edit{str(m, "old_string"), str(m, "new_string")})
		}
	}
	var out []wire.Hunk
	for _, e := range edits {
		var h wire.Hunk
		if e.old != "" {
			for _, l := range strings.Split(e.old, "\n") {
				h.Lines = append(h.Lines, "-"+l)
				h.OldLines++
			}
		}
		if e.new != "" {
			for _, l := range strings.Split(e.new, "\n") {
				h.Lines = append(h.Lines, "+"+l)
				h.NewLines++
			}
		}
		if len(h.Lines) > 400 {
			h.Lines = h.Lines[:400]
		}
		out = append(out, h)
	}
	return out
}

func todos(v any) []wire.TodoItem {
	list, _ := v.([]any)
	var out []wire.TodoItem
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, wire.TodoItem{Content: str(m, "content"), Status: str(m, "status")})
	}
	return out
}

// toolResult completes the tool call a result answers.
func (c *claude) toolResult(b claudeBlock, structured json.RawMessage) *Change {
	it := c.tools[b.ToolUseID]
	if it == nil {
		return nil
	}
	t := it.Tool
	result := resultText(b.Content)
	switch {
	case b.IsError && (strings.Contains(result, "doesn't want to proceed") || strings.Contains(result, "was rejected") || strings.Contains(result, "denied")):
		t.Status = wire.ToolDenied
	case b.IsError:
		t.Status = wire.ToolFailed
	default:
		t.Status = wire.ToolOK
	}
	sr := inputMap(structured)
	switch t.View {
	case wire.ToolEdit, wire.ToolWrite:
		if hunks := patchHunks(sr["structuredPatch"]); len(hunks) > 0 {
			t.Edit.Hunks = hunks
		} else if t.View == wire.ToolWrite {
			if content := str(sr, "content"); content != "" {
				t.Edit.Hunks = []wire.Hunk{newFileHunk(content)}
			}
		}
		if str(sr, "type") == "update" {
			t.Edit.Created = false
		}
	case wire.ToolBash:
		out := str(sr, "stdout")
		if e := str(sr, "stderr"); e != "" {
			out = strings.TrimRight(out, "\n") + "\n" + e
		}
		if out == "" {
			out = result
		}
		t.Bash.Output, t.Truncated = clipTail(strings.TrimRight(out, "\n"), maxResult)
		if interrupted, _ := sr["interrupted"].(bool); interrupted {
			t.Status = wire.ToolInterrupted
		}
	case wire.ToolTodo:
		if nt := todos(sr["newTodos"]); nt != nil {
			t.Todos = nt
		}
	case wire.ToolAsk:
		t.Answers = answersOf(sr)
	case wire.ToolPlan:
		if p := str(sr, "plan"); p != "" {
			t.Plan = p // as approved, edits included
		}
	}
	if t.View != wire.ToolBash {
		t.Result, t.Truncated = clip(result, maxResult)
	}
	return &Change{Op: wire.ChatUpdate, Item: *it}
}

// resultText is a tool result's text: a string, or the text of its blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func patchHunks(v any) []wire.Hunk {
	list, _ := v.([]any)
	var out []wire.Hunk
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		num := func(k string) int { f, _ := m[k].(float64); return int(f) }
		h := wire.Hunk{OldStart: num("oldStart"), OldLines: num("oldLines"), NewStart: num("newStart"), NewLines: num("newLines")}
		lines, _ := m["lines"].([]any)
		for _, l := range lines {
			if s, ok := l.(string); ok {
				h.Lines = append(h.Lines, s)
			}
		}
		out = append(out, h)
	}
	return out
}

func newFileHunk(content string) wire.Hunk {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > 400 {
		lines = lines[:400]
	}
	h := wire.Hunk{NewStart: 1, NewLines: len(lines)}
	for _, l := range lines {
		h.Lines = append(h.Lines, "+"+l)
	}
	return h
}

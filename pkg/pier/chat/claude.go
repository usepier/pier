package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// claude reads Claude Code's session transcripts
// (~/.claude/projects/<project>/<session>.jsonl): one JSON object per line,
// "user" and "assistant" lines carrying API-style messages, tool results
// arriving as user lines with a structured toolUseResult beside them.
type claude struct {
	tools   map[string]*wire.ChatItem
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
			if ch := c.toolResult(b, l.ToolUseResult); ch != nil {
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
	return []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: l.UUID, Kind: wire.ChatUser, Time: at, Text: t, Images: images, Sidechain: l.IsSidechain}}}
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
	for i, b := range blocks {
		// A message's blocks arrive one line each; the uuid keeps a block's
		// id unique even when lines repeat a message id.
		id := fmt.Sprintf("%s:%d", l.UUID, i)
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
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: id, Kind: wire.ChatAssistant, Time: at, Text: t, Model: m.Model, Sidechain: l.IsSidechain}})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			t, _ := clip(b.Thinking, maxText)
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: id, Kind: wire.ChatThinking, Time: at, Text: t, Sidechain: l.IsSidechain}})
		case "tool_use":
			it := &wire.ChatItem{ID: b.ID, Kind: wire.ChatTool, Time: at, Sidechain: l.IsSidechain, Tool: claudeTool(b)}
			c.tools[b.ID] = it
			out = append(out, Change{Op: wire.ChatAppend, Item: *it})
		}
	}
	return out
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
		t.Edit = &wire.EditView{Path: p}
	case "Write":
		t.View = wire.ToolWrite
		t.Summary = str(in, "file_path")
		t.Edit = &wire.EditView{Path: t.Summary, Created: true}
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
	default:
		t.Summary = b.Name
	}
	return t
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

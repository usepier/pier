package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

// CodexThread reads one thread of codex's app server (codex app-server):
// its item notifications into chat items, its deltas into streaming text.
// Items keep codex's own ids, so an item started, streamed and completed
// stays one item.
type CodexThread struct {
	prefix string
	n      int
	// tools are the tool items still running, by id.
	tools map[string]*wire.ChatItem
	// Prompts: user messages are the chat's own items (it sends them), so
	// the thread's echo of them is skipped — unless importing a thread
	// whose prompts the chat never saw.
	Prompts bool
	// Plan is the plan item the turn proposed, if any.
	Plan string
}

// NewCodexThread starts reading a codex thread; prefix as for
// NewClaudeStream.
func NewCodexThread(prefix string) *CodexThread {
	return &CodexThread{prefix: prefix, tools: map[string]*wire.ChatItem{}}
}

type codexItem struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
	Summary []string        `json:"summary"`
	// commandExecution
	Command          string `json:"command"`
	Status           string `json:"status"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	CommandActions   []struct {
		Type  string `json:"type"`
		Path  string `json:"path"`
		Query string `json:"query"`
		Name  string `json:"name"`
	} `json:"commandActions"`
	// fileChange
	Changes []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
		Diff string `json:"diff"`
	} `json:"changes"`
	// mcpToolCall, dynamicToolCall
	Server    string          `json:"server"`
	Tool      json.RawMessage `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
	// webSearch
	Query string `json:"query"`
	// imageView
	Path string `json:"path"`
	// collabAgentToolCall
	Prompt string `json:"prompt"`
	// review mode
	Review string `json:"review"`
}

type codexParams struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	ItemID   string          `json:"itemId"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	WillRetry   bool   `json:"willRetry"`
	Explanation string `json:"explanation"`
	Plan        []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	} `json:"plan"`
}

// Feed reads one notification of the thread.
func (c *CodexThread) Feed(method string, params json.RawMessage) Event {
	var p codexParams
	if json.Unmarshal(params, &p) != nil {
		return Event{}
	}
	switch method {
	case "item/agentMessage/delta":
		return Event{Deltas: []wire.ChatDelta{{ID: p.ItemID, Kind: wire.ChatAssistant, Text: p.Delta}}}
	case "item/reasoning/summaryTextDelta":
		return Event{Deltas: []wire.ChatDelta{{ID: p.ItemID, Kind: wire.ChatThinking, Text: p.Delta}}}
	case "item/plan/delta":
		return Event{Deltas: []wire.ChatDelta{{ID: p.ItemID, Kind: wire.ChatPlan, Text: p.Delta}}}
	case "item/started", "item/completed":
		var it codexItem
		if json.Unmarshal(p.Item, &it) != nil {
			return Event{}
		}
		return Event{Changes: c.item(it, method == "item/completed")}
	case "turn/started":
		c.Plan = ""
	case "turn/plan/updated":
		t := &wire.ToolCall{ID: "plan-" + p.TurnID, Name: "update_plan", View: wire.ToolTodo, Status: wire.ToolOK, Summary: "updated the plan"}
		if p.Explanation != "" {
			t.Summary = oneLine(p.Explanation)
		}
		for _, s := range p.Plan {
			t.Todos = append(t.Todos, wire.TodoItem{Content: s.Step, Status: todoStatus(s.Status)})
		}
		return Event{Changes: []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: t.ID, Kind: wire.ChatTool, Time: now(), Tool: t}}}}
	case "error":
		if p.WillRetry || p.Error == nil {
			return Event{}
		}
		return Event{Changes: []Change{{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.nextID("e"), Kind: wire.ChatError, Time: now(), Text: p.Error.Message}}}}
	case "turn/completed":
		end := &TurnEnd{}
		var out []Change
		switch p.Turn.Status {
		case "interrupted":
			end.Interrupted = true
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.nextID("s"), Kind: wire.ChatStatus, Time: now(), Text: "interrupted"}})
		case "failed":
			end.Error = "The turn failed."
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				end.Error = p.Turn.Error.Message
			}
			out = append(out, Change{Op: wire.ChatAppend, Item: wire.ChatItem{ID: c.nextID("e"), Kind: wire.ChatError, Time: now(), Text: end.Error}})
		}
		for id, it := range c.tools {
			if it.Tool.Status == wire.ToolRunning {
				it.Tool.Status = wire.ToolInterrupted
				out = append([]Change{{Op: wire.ChatUpdate, Item: *it}}, out...)
			}
			delete(c.tools, id)
		}
		return Event{Changes: out, TurnEnd: end}
	}
	return Event{}
}

func todoStatus(s string) string {
	switch s {
	case "inProgress":
		return "in_progress"
	case "completed":
		return "completed"
	}
	return "pending"
}

func (c *CodexThread) nextID(prefix string) string {
	c.n++
	return fmt.Sprintf("%s%s%d", c.prefix, prefix, c.n)
}

// item turns a thread item into chat changes: done says it completed.
func (c *CodexThread) item(it codexItem, done bool) []Change {
	at := now()
	op := wire.ChatAppend
	put := func(item wire.ChatItem) []Change { return []Change{{Op: op, Item: item}} }
	switch it.Type {
	case "userMessage":
		if !c.Prompts || !done {
			return nil
		}
		text, files := userContent(it.Content)
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatUser, Time: at, Text: text, Attachments: files})
	case "agentMessage":
		if !done || strings.TrimSpace(it.Text) == "" {
			return nil
		}
		t, _ := clip(it.Text, maxText)
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatAssistant, Time: at, Text: t})
	case "reasoning":
		text := strings.TrimSpace(strings.Join(it.Summary, "\n\n"))
		if !done || text == "" {
			return nil
		}
		t, _ := clip(text, maxText)
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatThinking, Time: at, Text: t})
	case "plan":
		if !done {
			return nil
		}
		c.Plan = it.Text
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatPlan, Time: at, Text: it.Text})
	case "commandExecution":
		return c.tool(it.ID, done, codexCommand(it))
	case "fileChange":
		return c.tool(it.ID, done, codexFileChange(it))
	case "mcpToolCall", "dynamicToolCall":
		return c.tool(it.ID, done, codexMCP(it))
	case "webSearch":
		t := &wire.ToolCall{ID: it.ID, Name: "web_search", View: wire.ToolWeb, Status: wire.ToolOK, Summary: it.Query}
		if !done {
			t.Status = wire.ToolRunning
		}
		return c.tool(it.ID, done, t)
	case "collabAgentToolCall":
		t := &wire.ToolCall{ID: it.ID, Name: "agent", View: wire.ToolTask, Status: codexStatus(it.Status, done), Summary: oneLine(it.Prompt)}
		return c.tool(it.ID, done, t)
	case "imageView":
		if !done {
			return nil
		}
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatStatus, Time: at, Text: "looked at " + it.Path})
	case "contextCompaction":
		if !done {
			return nil
		}
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatStatus, Time: at, Text: "conversation compacted"})
	case "enteredReviewMode", "exitedReviewMode":
		if !done {
			return nil
		}
		text := "started a review"
		if it.Type == "exitedReviewMode" {
			text = "finished the review"
		}
		return put(wire.ChatItem{ID: it.ID, Kind: wire.ChatStatus, Time: at, Text: text})
	}
	return nil
}

// tool records a tool item, started or done.
func (c *CodexThread) tool(id string, done bool, t *wire.ToolCall) []Change {
	if t == nil {
		return nil
	}
	if prev := c.tools[id]; prev != nil {
		prev.Tool = t
		if done {
			delete(c.tools, id)
		}
		return []Change{{Op: wire.ChatUpdate, Item: *prev}}
	}
	item := &wire.ChatItem{ID: id, Kind: wire.ChatTool, Time: now(), Tool: t}
	if !done {
		c.tools[id] = item
	}
	return []Change{{Op: wire.ChatAppend, Item: *item}}
}

func codexStatus(s string, done bool) wire.ToolStatus {
	switch s {
	case "completed":
		return wire.ToolOK
	case "failed":
		return wire.ToolFailed
	case "declined":
		return wire.ToolDenied
	}
	if done {
		return wire.ToolOK
	}
	return wire.ToolRunning
}

// shellWrapper is how codex runs a command: through a login shell.
var shellWrapper = regexp.MustCompile(`(?s)^/(?:usr/)?bin/(?:ba|z)?sh -l?c (?:'(.*)'|"(.*)")$`)

// UnwrapShell is the command as the agent wrote it, without the shell
// codex runs it through.
func UnwrapShell(cmd string) string {
	if m := shellWrapper.FindStringSubmatch(cmd); m != nil {
		if m[1] != "" {
			return strings.ReplaceAll(m[1], `'\''`, "'")
		}
		return m[2]
	}
	return cmd
}

func codexCommand(it codexItem) *wire.ToolCall {
	cmd := UnwrapShell(it.Command)
	done := it.Status != "" && it.Status != "inProgress"
	t := &wire.ToolCall{ID: it.ID, Name: "shell", View: wire.ToolBash, Status: codexStatus(it.Status, done), Summary: oneLine(cmd)}
	t.Bash = &wire.BashView{Command: cmd, ExitCode: it.ExitCode}
	// A command that only reads or searches shows as what it does.
	if len(it.CommandActions) == 1 {
		a := it.CommandActions[0]
		switch a.Type {
		case "read":
			t.View, t.Summary = wire.ToolRead, a.Path
		case "search":
			t.View, t.Summary = wire.ToolSearch, a.Query
			if a.Path != "" {
				t.Summary += " in " + a.Path
			}
		}
	}
	if done {
		out := strings.TrimRight(it.AggregatedOutput, "\n")
		if t.View == wire.ToolBash {
			t.Bash.Output, t.Truncated = clipTail(out, maxResult)
		} else {
			t.Result, t.Truncated = clip(out, maxResult)
		}
		if t.Status == wire.ToolOK && it.ExitCode != nil && *it.ExitCode != 0 {
			t.Status = wire.ToolFailed
		}
	}
	return t
}

func codexFileChange(it codexItem) *wire.ToolCall {
	done := it.Status != "" && it.Status != "inProgress"
	t := &wire.ToolCall{ID: it.ID, Name: "apply_patch", View: wire.ToolEdit, Status: codexStatus(it.Status, done)}
	if len(it.Changes) == 0 {
		t.Summary = "edited files"
		return t
	}
	ch := it.Changes[0]
	t.Edit = &wire.EditView{Path: ch.Path, Created: ch.Kind.Type == "add", Hunks: unifiedHunks(ch.Diff, ch.Kind.Type == "add")}
	if t.Edit.Created {
		t.View = wire.ToolWrite
	}
	t.Summary = ch.Path
	if n := len(it.Changes); n > 1 {
		t.Summary += fmt.Sprintf(" and %d more", n-1)
		var paths []string
		for _, c := range it.Changes[1:] {
			paths = append(paths, c.Path)
		}
		t.Result = "Also changed: " + strings.Join(paths, ", ")
	}
	return t
}

// unifiedHunks reads a unified diff's hunks; an added file's diff is its
// content.
func unifiedHunks(diff string, added bool) []wire.Hunk {
	var out []wire.Hunk
	var h *wire.Hunk
	for _, line := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			out = append(out, wire.Hunk{})
			h = &out[len(out)-1]
			fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &h.OldStart, &h.OldLines, &h.NewStart, &h.NewLines)
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
		case h != nil:
			h.Lines = append(h.Lines, line)
		case added:
			out = append(out, newFileHunk(diff))
			return out
		}
	}
	return out
}

func codexMCP(it codexItem) *wire.ToolCall {
	var name string
	if json.Unmarshal(it.Tool, &name) != nil {
		name = string(it.Tool)
	}
	if it.Server != "" {
		name = it.Server + "." + name
	}
	done := it.Status != "" && it.Status != "inProgress"
	t := &wire.ToolCall{ID: it.ID, Name: name, View: wire.ToolGeneric, Status: codexStatus(it.Status, done), Summary: name}
	if len(it.Arguments) > 0 && string(it.Arguments) != "null" {
		t.Input, _ = clip(string(it.Arguments), maxInput)
	}
	if len(it.Error) > 0 && string(it.Error) != "null" {
		t.Status = wire.ToolFailed
		t.Result, t.Truncated = clip(string(it.Error), maxResult)
	} else if len(it.Result) > 0 && string(it.Result) != "null" {
		t.Result, t.Truncated = clip(mcpText(it.Result), maxResult)
	}
	return t
}

// mcpText is a tool result's text content, else its JSON.
func mcpText(raw json.RawMessage) string {
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &r) == nil && len(r.Content) > 0 {
		var parts []string
		for _, c := range r.Content {
			if c.Type == "text" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}

// userContent reads a user message's text and attachments.
func userContent(raw json.RawMessage) (string, []wire.Attachment) {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &parts)
	var texts []string
	var files []wire.Attachment
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "localImage", "image":
			files = append(files, wire.Attachment{Name: baseName(p.Path), Path: p.Path, Mime: "image/*"})
		case "mention":
			files = append(files, wire.Attachment{Name: p.Name, Path: p.Path})
		}
	}
	t, _ := clip(strings.Join(texts, "\n"), maxText)
	return t, files
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	if p == "" {
		return "image"
	}
	return p
}

// CodexQuestions reads the questions of codex's request_user_input.
func CodexQuestions(params json.RawMessage) []wire.Question {
	var p struct {
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsOther  bool   `json:"isOther"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(params, &p)
	var out []wire.Question
	for _, q := range p.Questions {
		w := wire.Question{ID: q.ID, Question: q.Question, Header: q.Header, Kind: "choice", Other: q.IsOther, Secret: q.IsSecret}
		for _, o := range q.Options {
			w.Options = append(w.Options, wire.QuestionOption{Label: o.Label, Description: o.Description})
		}
		if len(w.Options) == 0 {
			w.Kind = "text"
		}
		out = append(out, w)
	}
	return out
}

// CodexCall is the tool call an approval request is about, as the chat
// shows it: the command, or the file changes of the item it names.
func CodexCall(method string, params json.RawMessage, item json.RawMessage) *wire.ToolCall {
	var it codexItem
	if len(item) > 0 {
		_ = json.Unmarshal(item, &it)
	}
	switch method {
	case "item/commandExecution/requestApproval":
		var p struct {
			ItemID  string `json:"itemId"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal(params, &p)
		if it.ID == "" {
			it = codexItem{ID: p.ItemID, Type: "commandExecution", Command: p.Command}
		}
		if p.Command != "" {
			it.Command = p.Command
		}
		it.Status = "inProgress"
		return codexCommand(it)
	case "item/fileChange/requestApproval":
		it.Status = "inProgress"
		return codexFileChange(it)
	}
	return nil
}

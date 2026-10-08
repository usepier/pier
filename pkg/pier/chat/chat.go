// Package chat reads an agent's transcript — the JSON lines the claude and
// codex CLIs write as they go — into chat items. The formats are the
// tools' own and change with their releases, so parsing is tolerant: lines
// of a kind it doesn't know are skipped, and a line it can't read at all
// marks the chat partial (the terminal view still shows everything).
package chat

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// Change is one effect of a transcript line on the chat.
type Change struct {
	Op   wire.ChatOp // append or update
	Item wire.ChatItem
}

// Parser turns transcript lines into chat changes.
type Parser interface {
	Feed(line []byte) []Change
	// Partial reports lines it couldn't read.
	Partial() bool
}

// NewParser returns the parser for an agent kind ("claude", "codex").
func NewParser(kind string) Parser {
	if kind == "codex" {
		return &codex{tools: map[string]*wire.ChatItem{}}
	}
	return &claude{tools: map[string]*wire.ChatItem{}}
}

// Limits on what an item carries: the chat summarizes, the terminal and
// the files hold the rest.
const (
	maxResult = 4000
	maxInput  = 2000
	maxText   = 64000
)

func clip(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	// Keep the end of output (where errors and results land), the start of
	// everything else.
	return s[:n] + "\n…", true
}

func clipTail(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := len(s) - n
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 && i < 200 {
		cut += i + 1
	}
	return "…\n" + s[cut:], true
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s, _ := clip(string(b), maxInput)
	return s
}

// oneLine trims a summary to one short line.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 200 {
		s = s[:197] + "…"
	}
	return s
}

// Summarize describes a tool call in one line — the command, the file, the
// pattern — for a permission request.
func Summarize(tool string, input json.RawMessage) string {
	t := claudeTool(claudeBlock{Name: tool, Input: input})
	return t.Summary
}

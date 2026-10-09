// Package chat reads what agents say into chat items: the stream claude
// writes running headless, the notifications of codex's app server, and
// the transcripts both CLIs keep (read when a chat was continued in a
// terminal). The formats are the tools' own and change with their
// releases, so reading is tolerant: messages of a kind it doesn't know are
// skipped, and one it can't read at all is counted.
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

// NewParser returns the transcript parser for an agent kind ("claude",
// "codex"). prefix goes before the ids of items with none of their own, so
// two readings of one transcript (from different offsets) never collide.
func NewParser(kind, prefix string) Parser {
	if kind == "codex" {
		return &codex{tools: map[string]*wire.ChatItem{}, prefix: prefix}
	}
	return &claude{tools: map[string]*wire.ChatItem{}}
}

// now is the time of items whose message has none; a var for tests.
var now = time.Now

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

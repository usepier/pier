package chat

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/usepier/pier/pkg/pier/wire"
)

// replay feeds a fixture through a parser and folds the changes into the
// final item list, as a client would.
func replay(t *testing.T, kind, file string) ([]wire.ChatItem, Parser) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := NewParser(kind)
	var items []wire.ChatItem
	index := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, c := range p.Feed(sc.Bytes()) {
			if i, ok := index[c.Item.ID]; ok {
				items[i] = c.Item
				continue
			}
			index[c.Item.ID] = len(items)
			items = append(items, c.Item)
		}
	}
	return items, p
}

func TestClaudeTranscript(t *testing.T) {
	items, p := replay(t, "claude", "claude/session.jsonl")
	if !p.Partial() {
		t.Error("an unreadable line must mark the chat partial")
	}
	var kinds []wire.ChatKind
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	want := []wire.ChatKind{
		wire.ChatUser, wire.ChatThinking, wire.ChatAssistant, wire.ChatTool, wire.ChatTool, wire.ChatTool, wire.ChatTool,
		wire.ChatStatus, wire.ChatStatus, wire.ChatUser, wire.ChatStatus, wire.ChatAssistant,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("item %d is %s, want %s (all: %v)", i, kinds[i], want[i], kinds)
		}
	}
	bash, edit, todo, denied := items[3].Tool, items[4].Tool, items[5].Tool, items[6].Tool
	if bash.View != wire.ToolBash || bash.Status != wire.ToolOK || bash.Bash.Output != "src/auth/mw.ts:12: redirect('/login')" || bash.Summary != "grep -rn redirect src/auth" {
		t.Errorf("bash = %+v %+v", bash, bash.Bash)
	}
	if edit.View != wire.ToolEdit || edit.Edit.Path != "/home/agent/work/shop/src/auth/mw.ts" || len(edit.Edit.Hunks) != 1 || edit.Edit.Hunks[0].Lines[2] != "+  if (!req.cookies.sid) {" {
		t.Errorf("edit = %+v", edit.Edit)
	}
	if todo.View != wire.ToolTodo || len(todo.Todos) != 2 || todo.Todos[1].Status != "in_progress" {
		t.Errorf("todo = %+v", todo.Todos)
	}
	if denied.Status != wire.ToolDenied {
		t.Errorf("a rejected call reads denied, got %s", denied.Status)
	}
	if items[7].Text != "interrupted" || items[8].Text != "ran /model" || items[9].Images != 1 || items[9].Text != "also check this" {
		t.Errorf("status/user items = %+v / %+v / %+v", items[7], items[8], items[9])
	}
	if items[10].Text != "conversation compacted" || items[11].Text != "Done — the redirect loop is fixed." {
		t.Errorf("tail = %+v %+v", items[10], items[11])
	}
}

func TestCodexRollout(t *testing.T) {
	items, p := replay(t, "codex", "codex/rollout.jsonl")
	if p.Partial() {
		t.Error("a clean rollout is not partial")
	}
	if len(items) != 7 {
		var kinds []wire.ChatKind
		for _, it := range items {
			kinds = append(kinds, it.Kind)
		}
		t.Fatalf("items = %v", kinds)
	}
	if items[0].Kind != wire.ChatUser || items[0].Text != "Add a test for the cart total" {
		t.Errorf("the environment context is not a prompt: %+v", items[0])
	}
	ls := items[2].Tool
	if ls.View != wire.ToolBash || ls.Bash.Command != "ls src/cart" || *ls.Bash.ExitCode != 0 || ls.Status != wire.ToolOK {
		t.Errorf("ls = %+v %+v", ls, ls.Bash)
	}
	patch := items[3].Tool
	if patch.View != wire.ToolEdit || patch.Edit.Path != "src/cart/total.test.ts" || len(patch.Edit.Hunks[0].Lines) != 2 {
		t.Errorf("patch = %+v", patch.Edit)
	}
	if test := items[4].Tool; test.Status != wire.ToolFailed || *test.Bash.ExitCode != 1 {
		t.Errorf("a failing command reads failed: %+v", test.Bash)
	}
	if items[5].Kind != wire.ChatAssistant || items[6].Text != "interrupted" {
		t.Errorf("tail = %+v %+v", items[5], items[6])
	}
}

func TestClipKeepsTheEndOfOutput(t *testing.T) {
	long := ""
	for i := 0; i < 2000; i++ {
		long += "line\n"
	}
	long += "ERROR: the end"
	got, cut := clipTail(long, 100)
	if !cut || len(got) > 110 || got[len(got)-14:] != "ERROR: the end" {
		t.Errorf("clipTail kept %q", got)
	}
}

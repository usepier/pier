package chat

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/pkg/pier/wire"
)

// The fixtures are real streams (claude 2.1.286, codex 0.159.3), recorded
// headless and scrubbed: {"dir": "in"|"out", "msg": ...} per line.

type fold struct {
	items     []wire.ChatItem
	index     map[string]int
	streaming map[string]string
	ends      []*TurnEnd
	session   string
}

func (f *fold) apply(ev Event) {
	if f.index == nil {
		f.index, f.streaming = map[string]int{}, map[string]string{}
	}
	for _, c := range ev.Changes {
		delete(f.streaming, c.Item.ID)
		if i, ok := f.index[c.Item.ID]; ok {
			f.items[i] = c.Item
			continue
		}
		f.index[c.Item.ID] = len(f.items)
		f.items = append(f.items, c.Item)
	}
	for _, d := range ev.Deltas {
		if _, done := f.index[d.ID]; !done {
			f.streaming[d.ID] += d.Text
		}
	}
	if ev.TurnEnd != nil {
		f.ends = append(f.ends, ev.TurnEnd)
		f.streaming = map[string]string{}
	}
	if ev.Session != "" {
		f.session = ev.Session
	}
}

func eachOut(t *testing.T, name string, fn func(msg json.RawMessage)) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "stream", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var r struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			t.Fatalf("bad fixture line %s", sc.Bytes())
		}
		if r.Dir == "out" {
			fn(r.Msg)
		}
	}
}

func claudeFold(t *testing.T, name string) *fold {
	s := NewClaudeStream("t-")
	f := &fold{}
	eachOut(t, name, func(m json.RawMessage) { f.apply(s.Feed(m)) })
	if s.Bad() != 0 {
		t.Errorf("%s: %d unreadable lines", name, s.Bad())
	}
	return f
}

func tools(items []wire.ChatItem) []*wire.ToolCall {
	var out []*wire.ToolCall
	for _, it := range items {
		if it.Tool != nil {
			out = append(out, it.Tool)
		}
	}
	return out
}

func TestClaudeStreamTools(t *testing.T) {
	f := claudeFold(t, "claude-tools.jsonl")
	if f.session == "" || len(f.ends) != 1 || f.ends[0].Error != "" {
		t.Fatalf("session %q, ends %+v", f.session, f.ends)
	}
	ts := tools(f.items)
	if len(ts) != 4 {
		t.Fatalf("tools = %d: %+v", len(ts), ts)
	}
	write, read, bash, edit := ts[0], ts[1], ts[2], ts[3]
	if write.View != wire.ToolWrite || write.Status != wire.ToolOK || !strings.HasSuffix(write.Edit.Path, "hello.txt") {
		t.Errorf("write = %+v", write)
	}
	if read.View != wire.ToolRead || read.Status != wire.ToolOK {
		t.Errorf("read = %+v", read)
	}
	if bash.View != wire.ToolBash || bash.Bash.Command != "ls && cat hello.txt" || !strings.Contains(bash.Bash.Output, "hi") {
		t.Errorf("bash = %+v %+v", bash, bash.Bash)
	}
	if edit.View != wire.ToolEdit || edit.Status != wire.ToolOK || len(edit.Edit.Hunks) == 0 {
		t.Errorf("edit = %+v", edit)
	}
	last := f.items[len(f.items)-1]
	if last.Kind != wire.ChatAssistant || last.Text != "done" {
		t.Errorf("last item = %+v", last)
	}
	// The text streamed in under the id its finished item has.
	for _, it := range f.items {
		if it.Kind == wire.ChatAssistant && !strings.HasPrefix(it.ID, "msg_") {
			t.Errorf("assistant item id %q isn't its message's", it.ID)
		}
	}
	if len(f.streaming) != 0 {
		t.Errorf("streaming left over: %v", f.streaming)
	}
}

func TestClaudeStreamDeltasMatchItems(t *testing.T) {
	s := NewClaudeStream("t-")
	deltas := map[string]string{}
	finals := map[string]string{}
	eachOut(t, "claude-tools.jsonl", func(m json.RawMessage) {
		ev := s.Feed(m)
		for _, d := range ev.Deltas {
			deltas[d.ID] += d.Text
		}
		for _, c := range ev.Changes {
			if c.Item.Kind == wire.ChatAssistant {
				finals[c.Item.ID] = c.Item.Text
			}
		}
	})
	if len(deltas) == 0 {
		t.Fatal("no deltas")
	}
	for id, text := range deltas {
		if finals[id] != text {
			t.Errorf("delta %s = %q, its item says %q", id, text, finals[id])
		}
	}
}

func TestClaudeStreamQuestionAndPlan(t *testing.T) {
	f := claudeFold(t, "claude-question.jsonl")
	var ask *wire.ToolCall
	for _, tc := range tools(f.items) {
		if tc.View == wire.ToolAsk {
			ask = tc
		}
	}
	if ask == nil || len(ask.Questions) != 2 || ask.Answers["Which color do you prefer?"] != "Blue" || ask.Questions[1].MultiSelect != true {
		t.Fatalf("ask = %+v", ask)
	}
	if ask.Questions[0].ID != ask.Questions[0].Question {
		t.Error("a claude question is keyed by its text")
	}

	f = claudeFold(t, "claude-plan.jsonl")
	var plans []*wire.ToolCall
	for _, tc := range tools(f.items) {
		if tc.View == wire.ToolPlan {
			plans = append(plans, tc)
		}
	}
	if len(plans) != 2 || plans[0].Status != wire.ToolFailed || plans[1].Status != wire.ToolOK || !strings.Contains(plans[1].Plan, "docstring") {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestClaudeStreamInterrupt(t *testing.T) {
	f := claudeFold(t, "claude-interrupt.jsonl")
	if len(f.ends) != 2 || !f.ends[0].Interrupted || f.ends[0].Error != "" || f.ends[1].Interrupted {
		t.Fatalf("ends = %+v %+v", f.ends[0], f.ends)
	}
	var sleep *wire.ToolCall
	for _, tc := range tools(f.items) {
		if tc.View == wire.ToolBash && sleep == nil {
			sleep = tc
		}
	}
	if sleep == nil || sleep.Status != wire.ToolDenied && sleep.Status != wire.ToolInterrupted {
		t.Errorf("the interrupted command = %+v", sleep)
	}
}

func codexFold(t *testing.T, name string) (*fold, *CodexThread) {
	c := NewCodexThread("t-")
	f := &fold{}
	eachOut(t, name, func(m json.RawMessage) {
		var n struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(m, &n)
		if n.Method == "" || len(n.ID) > 0 {
			return // responses and server requests are the host's
		}
		f.apply(c.Feed(n.Method, n.Params))
	})
	return f, c
}

func TestCodexThreadTools(t *testing.T) {
	f, _ := codexFold(t, "codex-tools.jsonl")
	if len(f.ends) != 1 {
		t.Fatalf("ends = %+v", f.ends)
	}
	ts := tools(f.items)
	if len(ts) != 3 {
		t.Fatalf("tools = %+v", ts)
	}
	if ts[0].View != wire.ToolBash || ts[0].Bash.Command != "ls\ncat math.py" || ts[0].Status != wire.ToolOK {
		t.Errorf("first command = %+v %+v", ts[0], ts[0].Bash)
	}
	if ts[2].View != wire.ToolEdit || ts[2].Edit.Path != "/home/agent/work/tools/math.py" || len(ts[2].Edit.Hunks) != 1 || ts[2].Status != wire.ToolOK {
		t.Errorf("edit = %+v %+v", ts[2], ts[2].Edit)
	}
	last := f.items[len(f.items)-1]
	if last.Kind != wire.ChatAssistant || last.Text != "done" {
		t.Errorf("last = %+v", last)
	}
	for _, it := range f.items {
		if it.Kind == wire.ChatUser {
			t.Error("the thread's echo of a prompt is the chat's own item already")
		}
	}
}

func TestCodexThreadPlan(t *testing.T) {
	f, c := codexFold(t, "codex-question.jsonl")
	var plans []wire.ChatItem
	for _, it := range f.items {
		if it.Kind == wire.ChatPlan {
			plans = append(plans, it)
		}
	}
	if len(plans) != 1 || !strings.Contains(plans[0].Text, "mul(a, b)") {
		t.Fatalf("plans = %+v", plans)
	}
	if c.Plan != "" {
		t.Error("the second turn (implementing) proposed no plan")
	}
	if len(f.ends) != 2 {
		t.Errorf("ends = %d", len(f.ends))
	}
}

func TestCodexThreadInterrupt(t *testing.T) {
	f, _ := codexFold(t, "codex-interrupt.jsonl")
	if len(f.ends) != 2 || !f.ends[0].Interrupted {
		t.Fatalf("ends = %+v", f.ends)
	}
	sleep := tools(f.items)[0]
	if sleep.Status != wire.ToolInterrupted || sleep.Bash.Command != "sleep 20" {
		t.Errorf("the cut-off command = %+v", sleep)
	}
}

func TestCodexQuestions(t *testing.T) {
	var qs []wire.Question
	eachOut(t, "codex-question.jsonl", func(m json.RawMessage) {
		var n struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(m, &n)
		if n.Method == "item/tool/requestUserInput" {
			qs = CodexQuestions(n.Params)
		}
	})
	if len(qs) != 1 || qs[0].ID != "operation" || !qs[0].Other || len(qs[0].Options) != 2 || qs[0].Kind != "choice" {
		t.Fatalf("questions = %+v", qs)
	}
}

func TestUnwrapShell(t *testing.T) {
	for in, want := range map[string]string{
		`/bin/zsh -lc 'ls'`:                  "ls",
		`/bin/bash -lc "printf 'hi' > x"`:    "printf 'hi' > x",
		`/bin/zsh -lc 'echo '\''quoted'\'''`: "echo 'quoted'",
		"make test":                          "make test",
	} {
		if got := UnwrapShell(in); got != want {
			t.Errorf("UnwrapShell(%q) = %q, want %q", in, got, want)
		}
	}
}

var updateMock = flag.Bool("update-mock", false, "write the app's mock chats from the recorded streams")

// TestMockChats keeps the app's mock backend (macos/mock) on chats read
// from the real recorded streams: `go test ./pkg/pier/chat -run
// MockChats -update-mock` writes them.
func TestMockChats(t *testing.T) {
	out := map[string][]wire.ChatItem{}
	for _, name := range []string{"claude-tools", "claude-plan", "claude-question", "claude-interrupt"} {
		out[name] = claudeFold(t, name+".jsonl").items
	}
	for _, name := range []string{"codex-tools", "codex-question", "codex-interrupt"} {
		f, _ := codexFold(t, name+".jsonl")
		out[name] = f.items
	}
	for name, items := range out {
		if len(items) == 0 {
			t.Errorf("%s: no items", name)
		}
	}
	if !*updateMock {
		return
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "macos", "mock", "chats.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

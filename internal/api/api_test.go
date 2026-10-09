package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/github"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/gitview"
	"github.com/usepier/pier/pkg/pier/wire"
)

// fake is a Backend with canned data that records what the server asked.
type fake struct {
	mu         sync.Mutex
	sessions   []pier.Session
	listErr    error
	lists      int
	sweeps     int
	resumed    []string
	created    []pier.CreateRequest
	termScript string
	block      chan struct{} // Resume waits on it when set
	// logs are the chat log chunks FollowChats sends, a chunk per entry;
	// more feeds later messages (event name, data).
	logs    []pier.LogChunk
	more    chan [2]string
	chats   string // the chats topic's data
	prompts []string
	decided []string
	follows int
	// gitStates are what FollowGit pushes; kept what KeepSnapshot got.
	gitStates  []pier.GitState
	kept       []pier.GitState
	following  map[string]int // sessions whose checkout is followed now
	prReads    int
	ownPrompts config.Prompts
}

func (f *fake) Cloud() string { return "AWS test-1" }
func (f *fake) Config() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := config.Default()
	c.Prompts = f.ownPrompts
	return c
}
func (f *fake) Prompts(repo string) []pier.Prompt {
	cfg := f.Config()
	var out []pier.Prompt
	for _, k := range config.PromptKinds {
		out = append(out, pier.Prompt{Key: k.Key, Title: k.Title, Own: cfg.PromptOverride(repo, k.Key), Effective: cfg.Prompt(repo, k.Key)})
	}
	return out
}
func (f *fake) SetPrompt(repo, key, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := config.Config{Prompts: f.ownPrompts}
	err := c.SetPrompt(repo, key, text)
	f.ownPrompts = c.Prompts
	return err
}
func (f *fake) ListSessions(context.Context) ([]pier.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]pier.Session(nil), f.sessions...), f.listErr
}
func (f *fake) Sweep(_ context.Context, s []pier.Session) []pier.Session {
	f.mu.Lock()
	f.sweeps++
	f.mu.Unlock()
	return s
}
func (f *fake) Enrich(_ context.Context, s []pier.Session) []pier.Session {
	out := append([]pier.Session(nil), s...)
	for i := range out {
		if out[i].State == pier.StateRunning {
			out[i].State = pier.StateWorking
		}
	}
	return out
}
func (f *fake) ReposFor(s []pier.Session, roots []string) []pier.Repo {
	return []pier.Repo{{Name: "app", PoolSize: 1, Ready: 1, Sessions: 1, MonthlyUSD: 5.8}}
}
func (f *fake) Checkouts() []pier.Checkout { return nil }
func (f *fake) LinkCheckout(dir string) (pier.Checkout, error) {
	return pier.Checkout{Path: dir, Name: "app"}, nil
}
func (f *fake) UnlinkCheckout(string) error { return nil }
func (f *fake) Headroom(context.Context) (pier.Quota, error) {
	return pier.Quota{Used: 2, Limit: 32, Detail: "2/32 vCPUs"}, nil
}
func (f *fake) HasKey(pier.Session) bool                           { return true }
func (f *fake) Park(context.Context, pier.Session) error           { return nil }
func (f *fake) Remove(context.Context, pier.Session) error         { return nil }
func (f *fake) Keep(context.Context, pier.Session) error           { return pier.ErrParked }
func (f *fake) Resize(context.Context, pier.Session, string) error { return nil }
func (f *fake) Machines(pier.Session) []pier.Machine {
	return []pier.Machine{{Type: "t4g.large", CPU: "2", Mem: "8", Cost: "$0.07/h"}}
}
func (f *fake) SetPoolSize(_ context.Context, _ string, _ int, p pier.Progress) (string, error) {
	p(pier.Event{Kind: pier.EventStep, Message: "draining", Elapsed: 1500 * time.Millisecond})
	return "", nil
}
func (f *fake) DrainPool(context.Context, string, pier.Progress) (int, error) { return 2, nil }
func (f *fake) Settings() []pier.SettingValue {
	var out []pier.SettingValue
	cfg := config.Default()
	for _, fl := range config.Settings {
		if cfg.Visible(fl) {
			v := config.Get(cfg, fl.Key)
			out = append(out, pier.SettingValue{Field: fl, Value: v, Display: fl.Display(v)})
		}
	}
	return out
}
func (f *fake) Set(key, value string) error {
	cfg := config.Default()
	return config.Set(&cfg, key, value)
}
func (f *fake) MachineCatalog(string) []pier.Machine { return nil }
func (f *fake) Doctor(context.Context) []pier.Check {
	return []pier.Check{{Name: "aws cli", OK: true}, {Name: "ssm plugin", Detail: "missing"}}
}
func (f *fake) ReauthCommand(err error) *exec.Cmd {
	if err == nil {
		return nil
	}
	return exec.Command("sh", "-c", "echo 'Open https://device.example/verify'; echo 'code: ABCD-1234' >&2")
}
func (f *fake) SetupLog(_ context.Context, s pier.Session, lines int) (string, error) {
	return "… older lines trimmed — pier logs x prints everything\n==> installing\npier setup: done\n", nil
}
func (f *fake) StartCreate(req pier.CreateRequest) (pier.Job, error) {
	f.mu.Lock()
	f.created = append(f.created, req)
	f.mu.Unlock()
	return pier.Job{ID: "j1", Kind: pier.JobCreate, Session: req.Branch, State: pier.JobRunning}, nil
}
func (f *fake) StartBake(string, *bool) (pier.Job, error) {
	return pier.Job{ID: "j2", Kind: pier.JobBake}, nil
}
func (f *fake) StartFill(string) (pier.Job, error) {
	return pier.Job{ID: "j3", Kind: pier.JobFill}, nil
}
func (f *fake) StartResize(s pier.Session, t string) (pier.Job, error) {
	return pier.Job{ID: "j4", Kind: pier.JobResize, Session: s.Name}, nil
}
func (f *fake) DiskMonthlyUSD() float64 { return 3.8 }
func (f *fake) Close()                  {}
func (f *fake) OpenTerminal(_ context.Context, s pier.Session, tab, window string, cols, rows int) (*pier.Terminal, error) {
	script := f.termScript
	if script == "" {
		script = "printf ready; read line; echo got:$line"
	}
	return pier.LocalTerminal([]string{"sh", "-c", script}, cols, rows)
}
func (f *fake) Windows(context.Context, pier.Session) ([]pier.Window, error) {
	return []pier.Window{{ID: "@1", Index: 0, Name: "agent", Role: "agent", Active: true}, {ID: "@2", Index: 1, Name: "zsh"}}, nil
}
func (f *fake) NewWindow(_ context.Context, _ pier.Session, name string) (pier.Window, error) {
	return pier.Window{ID: "@3", Index: 2, Name: name}, nil
}
func (f *fake) KillWindow(context.Context, pier.Session, string) error { return nil }
func (f *fake) Follow(ctx context.Context, s pier.Session, topics []string, fn func(string, json.RawMessage)) error {
	for i := range 2 {
		fn("env", json.RawMessage(fmt.Sprintf(`{"windows":"@1|0|1|claude|agent|agent\n","containers":"{\"ID\":\"c1\",\"Names\":\"db\",\"State\":\"running\"}\n","processes":[{"pid":%d,"command":"node","cpu":12,"rss":1000}],"ports":[{"port":3000,"process":"node"}]}`, 100+i)))
	}
	<-ctx.Done()
	return ctx.Err()
}
func (f *fake) FollowChats(ctx context.Context, s pier.Session, topics []string, tails []pier.Tail, fn func(string, json.RawMessage)) error {
	f.mu.Lock()
	logs, chats, more := f.logs, f.chats, f.more
	f.follows++
	f.mu.Unlock()
	if chats != "" {
		fn("chats", json.RawMessage(chats))
	}
	from := map[string]pier.Tail{}
	for _, t := range tails {
		from[t.Chat] = t
	}
	for _, ch := range logs {
		t, ok := from[ch.Chat]
		if !ok {
			continue
		}
		if ch.File == t.File && ch.Next <= t.From {
			continue // the caller has it
		}
		b, _ := json.Marshal(ch)
		fn("log", b)
	}
	for {
		select {
		case m := <-more:
			fn(m[0], json.RawMessage(m[1]))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (f *fake) CreateChat(_ context.Context, s pier.Session, r pier.ChatRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := ""
	if r.Prompt != nil {
		text = r.Prompt.Text
	}
	f.prompts = append(f.prompts, s.ID+":new "+r.Agent+":"+text)
	return r.ID, nil
}
func (f *fake) SendPrompt(_ context.Context, s pier.Session, chatID string, p wire.ChatPrompt, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	note := s.ID + ":" + chatID + ":" + p.ID + ":" + p.Text
	for _, a := range p.Attachments {
		note += ":" + a.Path
	}
	if mode != "" {
		note += ":" + mode
	}
	f.prompts = append(f.prompts, note)
	return nil
}
func (f *fake) StartDeliver(s pier.Session, d pier.Delivery) (pier.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, fmt.Sprintf("deliver %s:%s:%s:%d files", s.ID, d.Chat.ID, d.Prompt.Text, len(d.Files)))
	return pier.Job{ID: "j-deliver", Kind: pier.JobDeliver, Session: s.Name, PID: 1}, nil
}
func (f *fake) Dequeue(context.Context, pier.Session, string, string) error { return nil }
func (f *fake) Interrupt(context.Context, pier.Session, string) error       { return nil }
func (f *fake) Decide(_ context.Context, _ pier.Session, chatID, id string, d pier.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	note := chatID + ":" + id + ":" + d.Behavior
	for q, a := range d.Answers {
		note += ":" + q + "=" + a
	}
	if d.Mode != "" {
		note += ":" + d.Mode
	}
	f.decided = append(f.decided, note)
	return nil
}
func (f *fake) ConfigureChat(context.Context, pier.Session, string, pier.ChatConfig) error {
	return nil
}
func (f *fake) CloseChat(context.Context, pier.Session, string) error { return nil }
func (f *fake) ChatPage(context.Context, pier.Session, string, string, int) ([]wire.ChatItem, bool, error) {
	return []wire.ChatItem{{ID: "old", Kind: wire.ChatUser, Text: "from the session"}}, false, nil
}
func (f *fake) ChatWindow(_ context.Context, _ pier.Session, chatID string, takeover bool) (string, error) {
	if takeover {
		return "@7", nil
	}
	return "@6", nil
}
func (f *fake) Models(_ context.Context, s *pier.Session, agent string) (wire.AgentModels, error) {
	return wire.AgentModels{Agent: agent, Models: []wire.ModelInfo{{ID: "opus", Name: "Opus"}}, Modes: pier.AgentModes(agent)}, nil
}
func (f *fake) Upload(_ context.Context, _ pier.Session, name string, data []byte) (string, error) {
	return "/home/agent/.pier/uploads/x-" + name, nil
}
func (f *fake) ListFiles(context.Context, pier.Session) ([]string, bool, error) {
	return []string{"go.mod", "src/main.go"}, false, nil
}
func (f *fake) Changes(_ context.Context, s pier.Session) (gitview.Changes, error) {
	return gitview.Changes{BaseRef: "origin/main", Branch: gitview.Branch{Head: "fix", Oid: "abc", Ahead: 1},
		Files: []gitview.File{{Path: "a.go", Status: gitview.Modified, Added: 2, Uncommitted: true}}}, nil
}
func (f *fake) Diff(_ context.Context, _ pier.Session, file gitview.File, _ int) (string, bool, error) {
	return "--- a/" + file.Path + "\n+++ b/" + file.Path + "\n", false, nil
}
func (f *fake) FollowGit(ctx context.Context, s pier.Session, fn func(pier.GitState)) error {
	f.mu.Lock()
	states := f.gitStates
	if f.following == nil {
		f.following = map[string]int{}
	}
	f.following[s.ID]++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.following[s.ID]--
		f.mu.Unlock()
	}()
	for _, g := range states {
		fn(g)
	}
	<-ctx.Done()
	return ctx.Err()
}
func (f *fake) KeepSnapshot(_ context.Context, _ pier.Session, g pier.GitState) error {
	f.mu.Lock()
	f.kept = append(f.kept, g)
	f.mu.Unlock()
	return nil
}
func (f *fake) FileAt(_ context.Context, _ pier.Session, path, at string) (pier.FileContent, error) {
	if at == "base" {
		return pier.FileContent{Path: path, Data: []byte("package old\n"), Size: 12}, nil
	}
	if path == "logo.png" {
		return pier.FileContent{Path: path, Data: []byte{0x89, 'P', 0, 1}, Sha: "s0"}, nil
	}
	return pier.FileContent{Path: path, Data: []byte("package a\n"), Size: 10, Sha: "s1"}, nil
}
func (f *fake) WriteFile(_ context.Context, _ pier.Session, _, _ string, _ []byte, base string) (string, error) {
	if base != "s1" {
		return "", pier.ErrConflict
	}
	return "s2", nil
}
func (f *fake) Search(_ context.Context, _ pier.Session, q pier.SearchQuery, fn func([]pier.Match, bool, bool)) error {
	if q.Query == "(" {
		return &pier.OpError{Code: "", Message: "regex parse error"}
	}
	fn([]pier.Match{{Path: "a.go", Line: 3, Col: 5, Text: "hello"}}, false, false)
	fn([]pier.Match{{Path: "b.go", Line: 1, Col: 1, Text: "hello"}}, true, false)
	return nil
}
func (f *fake) Commit(context.Context, pier.Session, string, string) (string, error) {
	return "def", nil
}
func (f *fake) Push(context.Context, pier.Session, string) error { return nil }
func (f *fake) PullRequests(_ context.Context, ss []pier.Session) (map[string]pier.PR, error) {
	f.mu.Lock()
	f.prReads++
	f.mu.Unlock()
	out := map[string]pier.PR{}
	for _, s := range ss {
		if s.Branch == "fix" {
			out[s.ID] = pier.PR{ID: "PR_1", Number: 7, State: "merged", HeadOid: "abc", Checks: []pier.PRCheck{{Name: "test", State: "failing", JobID: 5}}}
		}
	}
	return out, nil
}
func (f *fake) PullRequest(_ context.Context, _ pier.Session, n int) (pier.PRDetail, error) {
	return pier.PRDetail{PR: pier.PR{Number: n, Checks: []pier.PRCheck{{Name: "test", State: "failing", JobID: 5}}},
		Threads: []github.Thread{{Path: "a.go", Line: 3, Comments: []github.ThreadComment{{Author: "rev", Body: "rename this"}}}}}, nil
}
func (f *fake) MergePR(_ context.Context, _ pier.Session, _, _, head string) error {
	if head != "abc" {
		return pier.ErrHeadMoved
	}
	return nil
}
func (f *fake) RerunChecks(context.Context, pier.Session, int64) error { return nil }
func (f *fake) CheckLog(context.Context, pier.Session, int64, int) (string, error) {
	return "--- FAIL: TestLogin", nil
}
func (f *fake) GitHubLimit() github.RateLimit { return github.RateLimit{} }
func (f *fake) RunProxy(ctx context.Context, _ func(string) error, onChange func(pier.ProxyStatus)) error {
	onChange(pier.ProxyStatus{Sessions: []pier.ProxySession{{ID: "i-1", Name: "fix", Ports: []int{3000}}}})
	<-ctx.Done()
	return nil
}
func (f *fake) ForwardPort(context.Context, pier.Session, int, int) error { return nil }
func (f *fake) UnforwardPort(pier.Session, int, int) error                { return nil }
func (f *fake) ContainerAction(_ context.Context, _ pier.Session, c, action string) error {
	if action != "restart" {
		return errors.New("no")
	}
	return nil
}
func (f *fake) RenameWindow(context.Context, pier.Session, string, string) error { return nil }
func (f *fake) Resume(ctx context.Context, s pier.Session) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.resumed = append(f.resumed, s.ID)
	f.mu.Unlock()
	return nil
}

// client drives a server over pipes.
type client struct {
	t    *testing.T
	in   io.WriteCloser
	msgs chan map[string]any
	done chan error
	id   int
}

func start(t *testing.T, f *fake) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, msgs: make(chan map[string]any, 100), done: make(chan error, 1)}
	go func() {
		c.done <- Serve(context.Background(), Options{
			In: inR, Out: outW, Version: "test",
			Open: func() (Backend, error) {
				if f == nil {
					return nil, fmt.Errorf("%w at /x — run `pier setup` first", config.ErrNoConfig)
				}
				return f, nil
			},
		})
		outW.Close()
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("server wrote a non-JSON line: %q", sc.Text())
				continue
			}
			c.msgs <- m
		}
		close(c.msgs)
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after its input closed")
		}
	})
	return c
}

func (c *client) send(m map[string]any) {
	c.t.Helper()
	m["jsonrpc"] = "2.0"
	b, _ := json.Marshal(m)
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

// call sends a request and returns its response, collecting notifications
// that arrived first.
func (c *client) call(method string, params any) (resp map[string]any, notes []map[string]any) {
	c.t.Helper()
	c.id++
	id := c.id
	c.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		select {
		case m, ok := <-c.msgs:
			if !ok {
				c.t.Fatalf("%s: connection closed", method)
			}
			if mid, ok := m["id"].(float64); ok && int(mid) == id {
				return m, notes
			}
			notes = append(notes, m)
		case <-time.After(10 * time.Second):
			c.t.Fatalf("%s: no response", method)
		}
	}
}

func (c *client) next(method string) map[string]any {
	c.t.Helper()
	for {
		select {
		case m := <-c.msgs:
			if m["method"] == method {
				return m
			}
		case <-time.After(10 * time.Second):
			c.t.Fatalf("no %s notification", method)
		}
	}
}

func errKind(resp map[string]any) string {
	e, _ := resp["error"].(map[string]any)
	d, _ := e["data"].(map[string]any)
	k, _ := d["kind"].(string)
	return k
}

func TestInitialize(t *testing.T) {
	c := start(t, &fake{})
	resp, _ := c.call("initialize", map[string]any{"protocol": 1, "client": map[string]any{"name": "test"}})
	r := resp["result"].(map[string]any)
	if r["protocol"] != float64(wire.ProtocolVersion) || r["version"] != "test" || r["configured"] != true || r["cloud"] != "AWS test-1" {
		t.Errorf("initialize = %v", r)
	}
}

func TestInitializeBeforeSetup(t *testing.T) {
	c := start(t, nil)
	resp, _ := c.call("initialize", map[string]any{"protocol": 1})
	if r := resp["result"].(map[string]any); r["configured"] != false {
		t.Errorf("a machine without a config must read unconfigured: %v", r)
	}
	resp, _ = c.call("settings.get", nil)
	if errKind(resp) != string(wire.ErrNotSetUp) {
		t.Errorf("calls before setup must fail as not_set_up: %v", resp)
	}
}

func TestProtocolErrors(t *testing.T) {
	c := start(t, &fake{})
	resp, _ := c.call("no.such.method", nil)
	if e := resp["error"].(map[string]any); e["code"] != float64(-32601) || errKind(resp) != "method_not_found" {
		t.Errorf("unknown method = %v", resp)
	}
	resp, _ = c.call("sessions.resume", "not an object")
	if errKind(resp) != "invalid_params" {
		t.Errorf("bad params = %v", resp)
	}
	resp, _ = c.call("sessions.keep", map[string]any{"id": "i-gone"})
	if errKind(resp) != "not_found" {
		t.Errorf("unknown session = %v", resp)
	}
	c.in.Write([]byte("{not json\n"))
	if m := <-c.msgs; errKind(m) != "parse" {
		t.Errorf("a garbled line must answer parse error: %v", m)
	}
}

func TestErrorKinds(t *testing.T) {
	login := &driver.LoginError{Login: []string{"aws", "sso", "login"}, Err: errors.New("sso expired")}
	for _, c := range []struct {
		err  error
		kind wire.ErrorKind
	}{
		{fmt.Errorf("listing: %w", login), wire.ErrAuthExpired},
		{fmt.Errorf("x: %w", pier.ErrParked), wire.ErrParked},
		{fmt.Errorf("x: %w", pier.ErrNoKey), wire.ErrNoKey},
		{context.Canceled, wire.ErrCancelled},
		{&driver.CloudError{Code: "VcpuLimitExceeded", Err: errors.New("aws ec2 run-instances: limit")}, wire.ErrQuota},
		{&driver.CloudError{Code: "NoSuchEntity", Err: errors.New("aws iam get-role: gone")}, wire.ErrFailed},
		{&github.Error{Status: 401, Message: "refused"}, wire.ErrGitHubAuth},
		{fmt.Errorf("pull requests: %w", &github.Error{Status: 429, Message: "slow down"}), wire.ErrRateLimited},
		{&pier.OpError{Code: "no_checkout", Message: "the session has no checkout yet"}, wire.ErrNoCheckout},
		{errors.New("boom"), wire.ErrFailed},
	} {
		e := toRPCError(c.err)
		if e.Data.Kind != c.kind || e.Message != c.err.Error() {
			t.Errorf("%v → %s %q, want %s", c.err, e.Data.Kind, e.Message, c.kind)
		}
	}
	if got := toRPCError(login).Data.Login; strings.Join(got, " ") != "aws sso login" {
		t.Errorf("auth_expired must carry the login, got %v", got)
	}
}

// A watch answers with a snapshot, then pushes numbered events — never one
// before the reply.
func TestOverviewWatch(t *testing.T) {
	f := &fake{sessions: []pier.Session{
		{ID: "i-1", Name: "feat", Repo: "app", State: pier.StateRunning, CostNote: "~$0.04/h"},
		{ID: "i-2", Name: "old", Repo: "app", State: pier.StateParked, CostNote: "~$3-4/mo"},
		{ID: "i-3", Name: "pool-x", Repo: "app", State: pier.StateParked, PoolGen: "g"},
	}}
	c := start(t, f)
	resp, notes := c.call("overview.watch", nil)
	if len(notes) != 0 {
		t.Fatalf("events arrived before the watch reply: %v", notes)
	}
	r := resp["result"].(map[string]any)
	sub, _ := r["sub"].(string)
	snap := r["snapshot"].(map[string]any)
	if sub == "" || snap["cloud"] != "AWS test-1" {
		t.Fatalf("watch result = %v", r)
	}
	totals := snap["totals"].(map[string]any)
	if totals["running"] != float64(1) || totals["parked"] != float64(1) || totals["poolReady"] != float64(1) || totals["hourlyUsd"] != 0.04 {
		t.Errorf("totals = %v", totals)
	}
	sessions := snap["sessions"].([]any)
	if len(sessions) != 3 || sessions[0].(map[string]any)["state"] != "working" || sessions[2].(map[string]any)["pooled"] != true {
		t.Errorf("sessions = %v", sessions)
	}
	if snap["quota"].(map[string]any)["limit"] != float64(32) {
		t.Errorf("quota = %v", snap["quota"])
	}

	c.send(map[string]any{"method": "client.activity", "params": map[string]any{"focused": true}})
	ev := c.next("sub.event")
	p := ev["params"].(map[string]any)
	if p["sub"] != sub || p["seq"] != float64(1) || p["topic"] != "overview" {
		t.Errorf("first event = %v", p)
	}
	f.mu.Lock()
	sweeps := f.sweeps
	f.mu.Unlock()
	if sweeps != 1 {
		t.Errorf("a burst of refreshes must sweep once, swept %d times", sweeps)
	}
	c.send(map[string]any{"method": "sub.cancel", "params": map[string]any{"sub": sub}})
}

// A failed refresh keeps the last data and says why, with the login that
// fixes an expired one.
func TestOverviewCarriesTheRefreshError(t *testing.T) {
	f := &fake{listErr: &driver.LoginError{Login: []string{"gcloud", "auth", "login"}, Err: errors.New("expired")}}
	c := start(t, f)
	resp, _ := c.call("overview.get", nil)
	o := resp["result"].(map[string]any)
	e, _ := o["error"].(map[string]any)
	if e == nil || e["kind"] != "auth_expired" || fmt.Sprint(e["login"]) != "[gcloud auth login]" {
		t.Errorf("overview error = %v", o["error"])
	}
}

func TestCancelRequest(t *testing.T) {
	f := &fake{block: make(chan struct{}), sessions: []pier.Session{{ID: "i-1", State: pier.StateParked}}}
	c := start(t, f)
	c.call("overview.get", nil) // the session is known
	c.id++
	id := c.id
	c.send(map[string]any{"id": id, "method": "sessions.resume", "params": map[string]any{"id": "i-1"}})
	time.Sleep(100 * time.Millisecond)
	c.send(map[string]any{"method": "$/cancelRequest", "params": map[string]any{"id": id}})
	for m := range c.msgs {
		if mid, ok := m["id"].(float64); ok && int(mid) == id {
			if errKind(m) != "cancelled" {
				t.Errorf("a cancelled request must answer cancelled: %v", m)
			}
			return
		}
	}
	t.Fatal("no response to the cancelled request")
}

// Long requests narrate through $/progress tagged with their id.
func TestProgressNotifications(t *testing.T) {
	c := start(t, &fake{})
	resp, notes := c.call("pool.setSize", map[string]any{"path": t.TempDir(), "size": 0})
	if errKind(resp) != "not_in_repo" {
		t.Errorf("a path outside any repo must fail not_in_repo: %v", resp)
	}
	_ = notes
	dir := t.TempDir()
	exec.Command("git", "init", "-q", dir).Run()
	resp, notes = c.call("pool.setSize", map[string]any{"path": dir, "size": 0})
	if resp["error"] != nil || len(notes) != 1 {
		t.Fatalf("setSize = %v, notes %v", resp, notes)
	}
	p := notes[0]["params"].(map[string]any)
	ev := p["event"].(map[string]any)
	if notes[0]["method"] != "$/progress" || p["id"] != resp["id"] || ev["message"] != "draining" || ev["elapsedMs"] != float64(1500) {
		t.Errorf("progress = %v", notes[0])
	}
}

func TestSettings(t *testing.T) {
	c := start(t, &fake{})
	resp, _ := c.call("settings.get", nil)
	r := resp["result"].(map[string]any)
	if len(r["groups"].([]any)) == 0 || len(r["settings"].([]any)) == 0 {
		t.Fatalf("settings = %v", r)
	}
	resp, _ = c.call("settings.set", map[string]any{"key": "idle_timeout", "value": "not a duration"})
	if errKind(resp) != "invalid_params" {
		t.Errorf("a bad value must fail validation: %v", resp)
	}
	resp, _ = c.call("doctor.run", nil)
	checks := resp["result"].([]any)
	if len(checks) != 2 || checks[1].(map[string]any)["status"] != "fail" {
		t.Errorf("doctor = %v", checks)
	}
}

package main

// Codex chats run on one codex app server per session (codex app-server,
// JSON-RPC over stdio), a thread per chat. Turns carry the chat's model,
// effort and permission mode; approvals and questions arrive as requests
// from the server, answered by a frontend. A plan-mode turn ends with a
// plan, which the chat puts to its user the way claude's are: approve
// (the next turn implements it) or send it back with feedback.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

type codexServer struct {
	h *chatHost

	mu      sync.Mutex
	cmd     *exec.Cmd
	in      io.WriteCloser
	done    <-chan struct{}
	next    int64
	calls   map[int64]chan rpcResult
	threads map[string]*codexThread // by thread id
	stderr  *tailBuffer
	active  time.Time
	wmu     sync.Mutex
	startMu sync.Mutex
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// codexThread is one chat's thread on the server: the chat's agentProc.
type codexThread struct {
	s      *codexServer
	c      *chatState
	id     string
	reader *chat.CodexThread

	mu sync.Mutex
	// turn is the turn running, planning whether it is a plan-mode one.
	turn     string
	planning bool
	// model is the model the thread runs when the chat names none.
	model string
	// items are the tool items in flight, for approval cards.
	items map[string]json.RawMessage
	gone  bool
}

func (h *chatHost) codexServer() *codexServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codex == nil {
		h.codex = &codexServer{h: h, threads: map[string]*codexThread{}, calls: map[int64]chan rpcResult{}}
	}
	return h.codex
}

func (s *codexServer) alive() bool {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// start runs the server and greets it, unless it runs already.
func (s *codexServer) start() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.alive() {
		return nil
	}
	bin, note := s.h.program("codex")
	if bin == "" {
		return errors.New(note)
	}
	if note != "" {
		fmt.Println("pier-supervisor:", note)
	}
	cmd := exec.Command(bin, "app-server", "--listen", "stdio://")
	cmd.Dir = checkoutDir(s.h.home)
	cmd.Env = agentEnv(s.h.home)
	cmd.SysProcAttr = childAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderr := newTailBuffer(filepath.Join(s.h.home, ".pier", "codex-app-server.log"))
	cmd.Stderr = stderr
	// Held until the server is recorded, so its exit is never handled
	// before it is.
	s.mu.Lock()
	done, err := startPiped(cmd, s.read, func(err error) {
		why := stderr.tail()
		if err != nil && why == "" {
			why = err.Error()
		}
		s.exited(why)
	})
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.cmd, s.in, s.done, s.stderr, s.active = cmd, in, done, stderr, time.Now()
	s.mu.Unlock()
	if _, err := s.call("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "pier", "title": "pier", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, time.Minute); err != nil {
		killGroup(cmd)
		return fmt.Errorf("codex didn't start: %w", err)
	}
	s.send(map[string]any{"method": "initialized"})
	return nil
}

func (s *codexServer) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	in := s.in
	s.mu.Unlock()
	if in == nil {
		return errors.New("codex isn't running")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = in.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its result.
func (s *codexServer) call(method string, params any, wait time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	s.next++
	id := s.next
	ch := make(chan rpcResult, 1)
	s.calls[id] = ch
	done := s.done
	s.active = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.calls, id)
		s.mu.Unlock()
	}()
	if err := s.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-done:
		return nil, errors.New("codex exited")
	case <-time.After(wait):
		return nil, fmt.Errorf("%s: no answer", method)
	}
}

func (s *codexServer) respond(id json.RawMessage, result any) error {
	return s.send(map[string]any{"id": id, "result": result})
}

func (s *codexServer) respondError(id json.RawMessage, msg string) error {
	return s.send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": msg}})
}

func (s *codexServer) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			s.line(line)
		}
		if err != nil {
			return
		}
	}
}

func (s *codexServer) line(line []byte) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	var thread struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(m.Params, &thread)
	s.mu.Lock()
	t := s.threads[thread.ThreadID]
	s.active = time.Now()
	s.mu.Unlock()
	switch {
	case m.Method == "" && len(m.ID) > 0:
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		s.mu.Lock()
		ch := s.calls[id]
		s.mu.Unlock()
		if ch != nil {
			r := rpcResult{result: m.Result}
			if m.Error != nil {
				r.err = errors.New(m.Error.Message)
			}
			ch <- r
		}
	case len(m.ID) > 0:
		if t == nil {
			s.respondError(m.ID, "no such thread here")
			return
		}
		t.request(m.ID, m.Method, m.Params)
	case t != nil:
		t.notification(m.Method, m.Params)
	}
}

func (s *codexServer) exited(why string) {
	s.mu.Lock()
	threads := s.threads
	s.threads = map[string]*codexThread{}
	s.in = nil
	s.mu.Unlock()
	for _, t := range threads {
		t.mu.Lock()
		t.gone = true
		t.mu.Unlock()
		s.h.exited(t.c, t, why)
	}
}

// reapIfIdle stops the server when no chat has used it for idleStop.
func (s *codexServer) reapIfIdle() {
	s.mu.Lock()
	idle := time.Since(s.active) > idleStop
	cmd := s.cmd
	for _, t := range s.threads {
		t.mu.Lock()
		if t.turn != "" {
			idle = false
		}
		t.mu.Unlock()
	}
	s.mu.Unlock()
	if !idle || !s.alive() {
		return
	}
	s.mu.Lock()
	in := s.in
	s.mu.Unlock()
	if in != nil {
		in.Close()
	}
	go func() {
		time.Sleep(5 * time.Second)
		if s.alive() {
			killGroup(cmd)
		}
	}()
}

// thread loads a chat's thread: a new one for a new chat, its old one
// resumed otherwise.
func (s *codexServer) thread(c *chatState) (agentProc, error) {
	if err := s.start(); err != nil {
		return nil, err
	}
	s.h.mu.Lock()
	rec := c.rec
	s.h.mu.Unlock()
	approval, sandbox := codexPolicy(rec.Mode)
	params := map[string]any{"cwd": checkoutDir(s.h.home), "approvalPolicy": approval, "sandbox": sandboxMode(sandbox)}
	if rec.Model != "" {
		params["model"] = rec.Model
	}
	method := "thread/start"
	if rec.Conversation != "" {
		method = "thread/resume"
		params["threadId"] = rec.Conversation
		params["excludeTurns"] = true
	}
	res, err := s.call(method, params, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res, &r); err != nil || r.Thread.ID == "" {
		return nil, fmt.Errorf("%s: no thread in its answer", method)
	}
	t := &codexThread{s: s, c: c, id: r.Thread.ID, reader: chat.NewCodexThread(newOpID() + "-"), model: r.Model, items: map[string]json.RawMessage{}}
	s.mu.Lock()
	s.threads[t.id] = t
	s.mu.Unlock()
	s.h.mu.Lock()
	if c.rec.Conversation != t.id {
		c.rec.Conversation = t.id
	}
	c.rec.Started = true
	s.h.save(c)
	s.h.mu.Unlock()
	go s.h.cacheCodexModels(s)
	return t, nil
}

// codexPolicy is a permission mode as codex's approval policy and sandbox.
func codexPolicy(mode string) (approval string, sandbox map[string]any) {
	switch mode {
	case "readOnly":
		return "on-request", map[string]any{"type": "readOnly", "networkAccess": false}
	case "bypassPermissions":
		return "never", map[string]any{"type": "dangerFullAccess"}
	}
	// The session is a machine of its own: the repo and the network are
	// the agent's to use; anything else asks.
	return "on-request", map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": true, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
}

func sandboxMode(p map[string]any) string {
	switch p["type"] {
	case "readOnly":
		return "read-only"
	case "dangerFullAccess":
		return "danger-full-access"
	}
	return "workspace-write"
}

func (t *codexThread) alive() bool {
	t.mu.Lock()
	gone := t.gone
	t.mu.Unlock()
	return !gone && t.s.alive()
}

// input is a prompt as codex reads it: text, images by path, other files
// mentioned.
func codexInput(p prompt) []map[string]any {
	in := []map[string]any{}
	text := p.Text
	var others []string
	for _, a := range p.Attachments {
		if imageBlock(a) != nil || strings.HasPrefix(a.Mime, "image/") {
			in = append(in, map[string]any{"type": "localImage", "path": a.Path})
			continue
		}
		others = append(others, a.Path)
	}
	if len(others) > 0 {
		text = strings.TrimSpace(text + "\n\nAttached files:\n- " + strings.Join(others, "\n- "))
	}
	if text != "" {
		in = append([]map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}, in...)
	}
	return in
}

func (t *codexThread) prompt(p prompt, steer bool) error {
	t.mu.Lock()
	turn := t.turn
	t.mu.Unlock()
	if steer && turn != "" {
		_, err := t.s.call("turn/steer", map[string]any{"threadId": t.id, "expectedTurnId": turn, "input": codexInput(p)}, time.Minute)
		return err
	}
	t.s.h.mu.Lock()
	rec := t.c.rec
	t.s.h.mu.Unlock()
	approval, sandbox := codexPolicy(rec.Mode)
	model := rec.Model
	if model == "" {
		model = t.model
	}
	params := map[string]any{"threadId": t.id, "input": codexInput(p), "approvalPolicy": approval, "sandboxPolicy": sandbox}
	if rec.Model != "" {
		params["model"] = rec.Model
	}
	var effort any
	if rec.Effort != "" {
		params["effort"] = rec.Effort
		effort = rec.Effort
	}
	planning := rec.Mode == "plan"
	collab := "default"
	if planning {
		collab = "plan"
	}
	params["collaborationMode"] = map[string]any{"mode": collab, "settings": map[string]any{"model": model, "reasoning_effort": effort, "developer_instructions": nil}}
	res, err := t.s.call("turn/start", params, time.Minute)
	if err != nil {
		return err
	}
	var r struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(res, &r)
	t.mu.Lock()
	t.turn, t.planning = r.Turn.ID, planning
	t.mu.Unlock()
	return nil
}

func (t *codexThread) interrupt() error {
	t.mu.Lock()
	turn := t.turn
	t.mu.Unlock()
	if turn == "" {
		return nil
	}
	_, err := t.s.call("turn/interrupt", map[string]any{"threadId": t.id, "turnId": turn}, 30*time.Second)
	return err
}

// configure: codex reads the model, effort and mode with every turn.
func (t *codexThread) configure(chatRecord) error { return nil }

func (t *codexThread) stop() {
	t.s.call("thread/unsubscribe", map[string]any{"threadId": t.id}, 10*time.Second)
	t.s.mu.Lock()
	delete(t.s.threads, t.id)
	t.s.mu.Unlock()
	t.mu.Lock()
	t.gone = true
	t.mu.Unlock()
}

func (t *codexThread) notification(method string, params json.RawMessage) {
	switch method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(params, &p)
		t.mu.Lock()
		t.turn = p.Turn.ID
		t.mu.Unlock()
	case "item/started", "item/completed":
		var p struct {
			Item struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"item"`
		}
		var raw struct {
			Item json.RawMessage `json:"item"`
		}
		_ = json.Unmarshal(params, &p)
		_ = json.Unmarshal(params, &raw)
		if p.Item.Type == "commandExecution" || p.Item.Type == "fileChange" {
			t.mu.Lock()
			if method == "item/started" {
				t.items[p.Item.ID] = raw.Item
			} else {
				delete(t.items, p.Item.ID)
			}
			t.mu.Unlock()
		}
	}
	ev := t.reader.Feed(method, params)
	if ev.TurnEnd == nil {
		t.s.h.apply(t.c, ev)
		return
	}
	t.mu.Lock()
	planned := t.planning && !ev.TurnEnd.Interrupted && ev.TurnEnd.Error == "" && strings.TrimSpace(t.reader.Plan) != ""
	t.turn, t.planning = "", false
	t.items = map[string]json.RawMessage{}
	t.mu.Unlock()
	if !planned {
		t.s.h.apply(t.c, ev)
		return
	}
	// The plan is put to the user as the turn ends.
	ev.TurnEnd = nil
	t.s.h.apply(t.c, ev)
	t.s.h.endTurnAsking(t.c, t.planRequest(t.reader.Plan))
}

// planRequest puts a plan-mode turn's plan to the user.
func (t *codexThread) planRequest(plan string) *pendingReq {
	req := wire.PermissionRequest{ID: newOpID(), Kind: "plan", Tool: "Plan", Summary: "Implement the plan?", Plan: plan}
	return &pendingReq{req: req, answer: func(d decision) error {
		if d.Behavior == "deny" {
			if strings.TrimSpace(d.Message) == "" {
				t.s.h.settle(t.c, "")
				return nil
			}
			t.s.h.settle(t.c, "")
			return t.s.h.send(t.c.rec.ID, prompt{ID: newOpID(), Text: d.Message}, "queue")
		}
		mode := d.Mode
		if mode == "" || mode == "plan" {
			mode = "default"
		}
		if err := checkMode("codex", mode); err != nil {
			return err
		}
		t.s.h.settle(t.c, mode)
		return t.s.h.send(t.c.rec.ID, prompt{ID: newOpID(), Text: "Implement the plan."}, "queue")
	}}
}

func (t *codexThread) request(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var p struct {
			ItemID string `json:"itemId"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(params, &p)
		t.mu.Lock()
		item := t.items[p.ItemID]
		t.mu.Unlock()
		call := chat.CodexCall(method, params, item)
		tool := "Shell"
		if method == "item/fileChange/requestApproval" {
			tool = "Edit"
		}
		req := wire.PermissionRequest{ID: newOpID(), Kind: "tool", Tool: tool, Call: call, Reason: p.Reason, AllowSession: true}
		if call != nil {
			req.Summary = call.Summary
		}
		t.s.h.ask(t.c, &pendingReq{req: req, answer: func(d decision) error {
			decision := "accept"
			switch d.Behavior {
			case "allowSession":
				decision = "acceptForSession"
			case "deny":
				decision = "decline"
			}
			if err := t.s.respond(id, map[string]any{"decision": decision}); err != nil {
				return err
			}
			if d.Behavior == "deny" && strings.TrimSpace(d.Message) != "" {
				// Codex hears only "declined": the why follows as a steer.
				go t.s.h.send(t.c.rec.ID, prompt{ID: newOpID(), Text: d.Message}, "steer")
			}
			return nil
		}})
	case "item/permissions/requestApproval":
		var p struct {
			Reason      string          `json:"reason"`
			Permissions json.RawMessage `json:"permissions"`
		}
		_ = json.Unmarshal(params, &p)
		summary := "more permissions"
		if p.Reason != "" {
			summary = oneLineOf(p.Reason)
		}
		req := wire.PermissionRequest{ID: newOpID(), Kind: "tool", Tool: "Permissions", Summary: summary, Reason: p.Reason, AllowSession: true,
			Call: &wire.ToolCall{Name: "permissions", View: wire.ToolGeneric, Status: wire.ToolRunning, Summary: summary, Input: string(p.Permissions)}}
		t.s.h.ask(t.c, &pendingReq{req: req, answer: func(d decision) error {
			switch d.Behavior {
			case "deny":
				return t.s.respond(id, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
			case "allowSession":
				return t.s.respond(id, map[string]any{"permissions": p.Permissions, "scope": "session"})
			}
			return t.s.respond(id, map[string]any{"permissions": p.Permissions, "scope": "turn"})
		}})
	case "item/tool/requestUserInput":
		qs := chat.CodexQuestions(params)
		req := wire.PermissionRequest{ID: newOpID(), Kind: "question", Tool: "Question", Questions: qs}
		if len(qs) > 0 {
			req.Summary = qs[0].Question
		}
		t.s.h.ask(t.c, &pendingReq{req: req, answer: func(d decision) error {
			answers := map[string]any{}
			if d.Behavior != "deny" {
				for _, q := range qs {
					a, ok := d.Answers[q.ID]
					if !ok {
						continue
					}
					list := []string{a}
					if n := strings.TrimSpace(d.Notes[q.ID]); n != "" {
						list = append(list, "user_note: "+n)
					}
					answers[q.ID] = map[string]any{"answers": list}
				}
			}
			return t.s.respond(id, map[string]any{"answers": answers})
		}})
	case "mcpServer/elicitation/request":
		t.s.respond(id, map[string]any{"action": "decline", "content": nil, "_meta": nil})
	default:
		t.s.respondError(id, method+" isn't supported by this host")
	}
}

func oneLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s, 200)
}

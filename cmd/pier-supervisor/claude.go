package main

// A claude chat's program: claude in print mode with stream-json in and
// out, one process per chat. Prompts go in as user messages (one sent
// mid-turn joins the running turn); everything claude would ask in its
// terminal comes as a can_use_tool control request, answered by a
// frontend. The process stays up between turns and is stopped when idle;
// the next prompt resumes the conversation (--resume).

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

type claudeProc struct {
	h      *chatHost
	c      *chatState
	cmd    *exec.Cmd
	wmu    sync.Mutex
	in     io.WriteCloser
	stream *chat.ClaudeStream
	done   <-chan struct{}
	stderr *tailBuffer

	mu  sync.Mutex
	ctl map[string]chan ctlResult
	// The model, effort, fast mode and permission mode the process runs
	// with now.
	model, effort, mode string
	fast                bool
}

type ctlResult struct {
	data json.RawMessage
	err  error
}

// claudeArgs is the command line a chat's claude runs with.
func claudeArgs(rec chatRecord) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio", "--allow-dangerously-skip-permissions"}
	if rec.Started {
		args = append(args, "--resume", rec.Conversation)
	} else {
		args = append(args, "--session-id", rec.Conversation)
	}
	if rec.Model != "" {
		args = append(args, "--model", rec.Model)
	}
	if rec.Mode != "" {
		args = append(args, "--permission-mode", rec.Mode)
	}
	if rec.Effort == "ultracode" {
		// Ultracode is xhigh with dynamic workflows: it starts at xhigh and
		// is switched on once claude answers (startClaude).
		args = append(args, "--effort", "xhigh")
	} else if rec.Effort != "" {
		args = append(args, "--effort", rec.Effort)
	}
	if rec.Fast {
		args = append(args, "--settings", `{"fastMode":true}`)
	}
	return args
}

func startClaude(h *chatHost, c *chatState) (agentProc, error) {
	bin, note := h.program("claude")
	if bin == "" {
		return nil, errors.New(note)
	}
	h.mu.Lock()
	rec := c.rec
	h.mu.Unlock()
	if note != "" {
		c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: note})
	}
	cmd := exec.Command(bin, claudeArgs(rec)...)
	cmd.Dir = checkoutDir(h.home)
	cmd.Env = agentEnv(h.home)
	cmd.SysProcAttr = childAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	p := &claudeProc{h: h, c: c, cmd: cmd, in: in, stream: chat.NewClaudeStream(newOpID() + "-"),
		stderr: newTailBuffer(filepath.Join(c.dir, "agent.log")), ctl: map[string]chan ctlResult{},
		model: rec.Model, effort: rec.Effort, mode: rec.Mode, fast: rec.Fast}
	cmd.Stderr = p.stderr
	p.done, err = startPiped(cmd, p.read, func(err error) {
		why := p.stderr.tail()
		if err != nil && why == "" {
			why = err.Error()
		}
		h.exited(c, p, why)
	})
	if err != nil {
		return nil, err
	}
	// The handshake: it answers once it is ready, and says which models it
	// offers.
	res, err := p.request(map[string]any{"subtype": "initialize"}, time.Minute)
	if err != nil {
		p.stop()
		return nil, fmt.Errorf("claude didn't start: %w", err)
	}
	h.cacheClaudeModels(res)
	if rec.Effort == "ultracode" {
		if _, err := p.request(map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": "ultracode"}}, 30*time.Second); err != nil {
			c.log.put(wire.ChatItem{ID: newOpID(), Kind: wire.ChatStatus, Time: time.Now(), Text: "Ultracode isn't available here (" + err.Error() + "): running at Extra high."})
			p.mu.Lock()
			p.effort = "xhigh"
			p.mu.Unlock()
		}
	}
	return p, nil
}

func (p *claudeProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *claudeProc) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.in.Write(append(b, '\n'))
	return err
}

// request sends a control request and waits for its answer.
func (p *claudeProc) request(req map[string]any, wait time.Duration) (json.RawMessage, error) {
	id := newOpID()
	ch := make(chan ctlResult, 1)
	p.mu.Lock()
	p.ctl[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.ctl, id)
		p.mu.Unlock()
	}()
	if err := p.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.data, r.err
	case <-p.done:
		return nil, errors.New("the agent exited")
	case <-time.After(wait):
		return nil, errors.New("no answer")
	}
}

func (p *claudeProc) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			p.line(line)
		}
		if err != nil {
			return
		}
	}
}

func (p *claudeProc) line(line []byte) {
	var m struct {
		Type      string          `json:"type"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
		Response  struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.Type {
	case "control_response":
		p.mu.Lock()
		ch := p.ctl[m.Response.RequestID]
		p.mu.Unlock()
		if ch != nil {
			r := ctlResult{data: m.Response.Response}
			if m.Response.Subtype == "error" {
				r.err = errors.New(m.Response.Error)
			}
			ch <- r
		}
	case "control_request":
		p.controlRequest(m.RequestID, m.Request)
	case "control_cancel_request":
		p.h.cancel(p.c, m.RequestID)
	default:
		p.noteFast(line)
		p.h.apply(p.c, p.stream.Feed(line))
	}
}

// noteFast keeps what claude says of its fast mode at a turn's start and
// end: on, or off and why.
func (p *claudeProc) noteFast(line []byte) {
	if !bytes.Contains(line, []byte(`"fast_mode_state"`)) {
		return
	}
	var m struct {
		State  string `json:"fast_mode_state"`
		Reason string `json:"fast_mode_disabled_reason"`
	}
	if json.Unmarshal(line, &m) != nil || m.State == "" {
		return
	}
	reason := ""
	if m.State == "off" {
		reason = m.Reason
	}
	p.h.mu.Lock()
	changed := p.c.fastOff != reason
	p.c.fastOff = reason
	p.h.mu.Unlock()
	if changed {
		p.h.notify()
	}
}

// canUseTool is claude asking to use a tool.
type canUseTool struct {
	Subtype     string          `json:"subtype"`
	ToolName    string          `json:"tool_name"`
	DisplayName string          `json:"display_name"`
	Input       json.RawMessage `json:"input"`
	Suggestions json.RawMessage `json:"permission_suggestions"`
	Reason      string          `json:"decision_reason"`
	ToolUseID   string          `json:"tool_use_id"`
	NoAlways    bool            `json:"suppress_always_allow_rule"`
}

func (p *claudeProc) controlRequest(id string, raw json.RawMessage) {
	var r canUseTool
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	if r.Subtype != "can_use_tool" {
		// Nothing else is asked of a host that registered no hooks or
		// servers; say so rather than leave it waiting.
		p.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": id, "error": "not supported by this host"}})
		return
	}
	input := withPlan(p.h.home, r.ToolName, r.Input)
	kind := "tool"
	switch r.ToolName {
	case "AskUserQuestion":
		kind = "question"
	case "ExitPlanMode":
		kind = "plan"
	}
	call := chat.ClaudeCall(r.ToolUseID, r.ToolName, input)
	req := wire.PermissionRequest{ID: id, Kind: kind, Tool: r.ToolName, Summary: call.Summary, Call: call, Reason: stripANSI(r.Reason)}
	if r.DisplayName != "" {
		req.Tool = r.DisplayName
	}
	req.AllowSession = kind == "tool" && !r.NoAlways && len(r.Suggestions) > 0 && string(r.Suggestions) != "null"
	req.Title, req.Questions, req.Plan = chat.Asks(r.ToolName, input)
	answer := func(d decision) error {
		resp, mode, err := claudeDecision(input, r.Suggestions, kind, d)
		if err != nil {
			return err
		}
		if mode != "" {
			p.mu.Lock()
			p.mode = mode
			p.mu.Unlock()
			p.h.mu.Lock()
			p.c.rec.Mode = mode
			p.h.save(p.c)
			p.h.mu.Unlock()
		}
		return p.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
	}
	p.h.ask(p.c, &pendingReq{req: req, answer: answer})
}

// claudeDecision is claude's answer to a can_use_tool request, and the
// permission mode it moves to (an approved plan leaves plan mode).
func claudeDecision(input, suggestions json.RawMessage, kind string, d decision) (map[string]any, string, error) {
	if d.Behavior == "deny" {
		msg := d.Message
		if msg == "" {
			switch kind {
			case "question":
				msg = "The user didn't answer your questions. Ask again in plain text if you still need the answers."
			default:
				msg = "The user declined this."
			}
		}
		return map[string]any{"behavior": "deny", "message": msg}, "", nil
	}
	in := map[string]any{}
	if len(input) > 0 && json.Unmarshal(input, &in) != nil {
		return nil, "", errors.New("the request's input isn't an object")
	}
	resp := map[string]any{"behavior": "allow"}
	mode := ""
	switch kind {
	case "question":
		if len(d.Answers) == 0 {
			return nil, "", errors.New("no answers")
		}
		in["answers"] = d.Answers
		if len(d.Notes) > 0 {
			ann := map[string]any{}
			for q, n := range d.Notes {
				ann[q] = map[string]string{"notes": n}
			}
			in["annotations"] = ann
		}
	case "plan":
		mode = d.Mode
		if mode == "" {
			mode = "default"
		}
		if err := checkMode("claude", mode); err != nil || mode == "plan" {
			return nil, "", fmt.Errorf("can't approve a plan into mode %q", mode)
		}
		resp["updatedPermissions"] = []map[string]string{{"type": "setMode", "mode": mode, "destination": "session"}}
	default:
		if d.Behavior == "allowSession" && len(suggestions) > 0 {
			resp["updatedPermissions"] = suggestions
		}
	}
	resp["updatedInput"] = in
	return resp, mode, nil
}

// withPlan fills a plan approval's input with the plan when the agent left
// it in its plan file: the frontend shows it.
func withPlan(home, tool string, input json.RawMessage) json.RawMessage {
	if tool != "ExitPlanMode" {
		return input
	}
	in := map[string]any{}
	if json.Unmarshal(input, &in) != nil {
		return input
	}
	if p, _ := in["plan"].(string); p != "" {
		return input
	}
	path, _ := in["planFilePath"].(string)
	if path == "" || !strings.HasPrefix(filepath.Clean(path), filepath.Clean(home)+"/") {
		return input
	}
	if b, err := os.ReadFile(path); err == nil && len(b) < 1<<20 {
		in["plan"] = string(b)
	}
	out, _ := json.Marshal(in)
	return out
}

// userMessage is a prompt as claude reads it: its text, its images inline,
// other files by path.
func userMessage(p prompt) map[string]any {
	var blocks []map[string]any
	text := p.Text
	var others []string
	for _, a := range p.Attachments {
		if b := imageBlock(a); b != nil {
			blocks = append(blocks, b)
			continue
		}
		others = append(others, a.Path)
	}
	if len(others) > 0 {
		text = strings.TrimSpace(text + "\n\nAttached files:\n- " + strings.Join(others, "\n- "))
	}
	if text != "" {
		blocks = append([]map[string]any{{"type": "text", "text": text}}, blocks...)
	}
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": blocks}, "parent_tool_use_id": nil, "session_id": ""}
}

// imageBlock reads an attached image into a content block; nil for files
// that aren't images claude reads inline.
func imageBlock(a wire.Attachment) map[string]any {
	mt := a.Mime
	if mt == "" || mt == "image/*" {
		mt = mime.TypeByExtension(strings.ToLower(filepath.Ext(a.Path)))
	}
	switch mt {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil
	}
	b, err := os.ReadFile(a.Path)
	if err != nil || len(b) > 5<<20 {
		return nil
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": base64.StdEncoding.EncodeToString(b)}}
}

func (p *claudeProc) prompt(pr prompt, steer bool) error {
	return p.write(userMessage(pr))
}

func (p *claudeProc) interrupt() error {
	_, err := p.request(map[string]any{"subtype": "interrupt"}, 30*time.Second)
	return err
}

func (p *claudeProc) configure(rec chatRecord) error {
	p.mu.Lock()
	model, effort, mode, fast := p.model, p.effort, p.mode, p.fast
	p.mu.Unlock()
	var errs []error
	if rec.Model != model {
		if _, err := p.request(map[string]any{"subtype": "set_model", "model": rec.Model}, 30*time.Second); err != nil {
			errs = append(errs, fmt.Errorf("model: %w", err))
		} else {
			model = rec.Model
		}
	}
	if rec.Mode != mode {
		if _, err := p.request(map[string]any{"subtype": "set_permission_mode", "mode": rec.Mode}, 30*time.Second); err != nil {
			errs = append(errs, fmt.Errorf("mode: %w", err))
		} else {
			mode = rec.Mode
		}
	}
	if rec.Effort != effort {
		if _, err := p.request(map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": rec.Effort}}, 30*time.Second); err != nil {
			errs = append(errs, fmt.Errorf("effort: %w", err))
		} else {
			effort = rec.Effort
		}
	}
	if rec.Fast != fast {
		if _, err := p.request(map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"fastMode": rec.Fast}}, 30*time.Second); err != nil {
			errs = append(errs, fmt.Errorf("fast: %w", err))
		} else {
			fast = rec.Fast
		}
	}
	p.mu.Lock()
	p.model, p.effort, p.mode, p.fast = model, effort, mode, fast
	p.mu.Unlock()
	return errors.Join(errs...)
}

// stop closes claude's input (it finishes and exits), then kills what is
// left after a grace period.
func (p *claudeProc) stop() {
	p.wmu.Lock()
	p.in.Close()
	p.wmu.Unlock()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		killGroup(p.cmd)
		<-p.done
	}
}

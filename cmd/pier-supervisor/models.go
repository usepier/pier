package main

// The models an agent offers, as it last said: claude in its handshake,
// codex when asked (model/list). Kept in ~/.pier/models/<agent>.json for
// frontends' pickers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

func modelsPath(home, agent string) string {
	return filepath.Join(home, ".pier", "models", agent+".json")
}

func saveModels(home, agent string, ms []wire.ModelInfo) {
	if len(ms) == 0 {
		return
	}
	b, _ := json.Marshal(ms)
	_ = os.MkdirAll(filepath.Dir(modelsPath(home, agent)), 0o700)
	_ = writeAtomic(modelsPath(home, agent), b)
}

func loadModels(home, agent string) []wire.ModelInfo {
	b, err := os.ReadFile(modelsPath(home, agent))
	if err != nil {
		return nil
	}
	var ms []wire.ModelInfo
	_ = json.Unmarshal(b, &ms)
	return ms
}

func commandsPath(home, agent string) string {
	return filepath.Join(home, ".pier", "models", agent+"-commands.json")
}

// saveCommands keeps an agent's slash commands, as it last said them.
func saveCommands(home, agent string, cs []wire.AgentCommand) {
	b, _ := json.Marshal(cs)
	_ = os.MkdirAll(filepath.Dir(commandsPath(home, agent)), 0o700)
	_ = writeAtomic(commandsPath(home, agent), b)
}

func loadCommands(home, agent string) []wire.AgentCommand {
	var cs []wire.AgentCommand
	if b, err := os.ReadFile(commandsPath(home, agent)); err == nil {
		_ = json.Unmarshal(b, &cs)
	}
	return cs
}

func (h *chatHost) cacheClaudeModels(res json.RawMessage) {
	saveModels(h.home, "claude", chat.ClaudeModels(res))
	saveCommands(h.home, "claude", chat.ClaudeCommands(res))
}

var codexModelsOnce sync.Map // per server: asked once

func (h *chatHost) cacheCodexModels(s *codexServer) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if _, loaded := codexModelsOnce.LoadOrStore(done, true); loaded {
		return
	}
	res, err := s.call("model/list", map[string]any{"includeHidden": false}, time.Minute)
	if err != nil {
		codexModelsOnce.Delete(done)
		return
	}
	saveModels(h.home, "codex", chat.CodexModels(res))
	h.cacheCodexSkills(s)
}

// cacheCodexSkills keeps the skills codex finds for the checkout, as its
// slash commands.
func (h *chatHost) cacheCodexSkills(s *codexServer) {
	res, err := s.call("skills/list", map[string]any{"cwds": []string{checkoutDir(h.home)}}, time.Minute)
	if err != nil {
		return
	}
	saveCommands(h.home, "codex", chat.CodexCommands(res))
}

// models is what an agent offers, asking it when nothing is known yet.
func (h *chatHost) models(agent string) (map[string]any, error) {
	if ms := loadModels(h.home, agent); len(ms) > 0 {
		return map[string]any{"models": ms, "commands": loadCommands(h.home, agent)}, nil
	}
	switch agent {
	case "claude":
		if err := h.probeClaude(); err != nil {
			return nil, err
		}
	case "codex":
		s := h.codexServer()
		if err := s.start(); err != nil {
			return nil, err
		}
		h.cacheCodexModels(s)
	default:
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	return map[string]any{"models": loadModels(h.home, agent), "commands": loadCommands(h.home, agent)}, nil
}

// probeClaude starts claude only to hear its handshake.
func (h *chatHost) probeClaude() error {
	bin, note := h.program("claude")
	if bin == "" {
		return errors.New(note)
	}
	cmd := exec.Command(bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence")
	cmd.Dir = checkoutDir(h.home)
	cmd.Env = agentEnv(h.home)
	cmd.SysProcAttr = childAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		in.Close()
		killGroup(cmd)
		cmd.Wait()
	}()
	b, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "probe", "request": map[string]any{"subtype": "initialize"}})
	if _, err := in.Write(append(b, '\n')); err != nil {
		return err
	}
	found := make(chan json.RawMessage, 1)
	go func() {
		dec := json.NewDecoder(out)
		for {
			var m struct {
				Type     string `json:"type"`
				Response struct {
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
				} `json:"response"`
			}
			if dec.Decode(&m) != nil {
				close(found)
				return
			}
			if m.Type == "control_response" && m.Response.RequestID == "probe" {
				found <- m.Response.Response
				return
			}
		}
	}()
	select {
	case res, ok := <-found:
		if !ok {
			return errors.New("claude exited before its handshake")
		}
		h.cacheClaudeModels(res)
		return nil
	case <-time.After(time.Minute):
		return errors.New("claude didn't answer its handshake")
	}
}

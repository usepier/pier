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

// claudeModels reads the models of claude's initialize answer.
func claudeModels(res json.RawMessage) []wire.ModelInfo {
	var r struct {
		Models []struct {
			Value         string   `json:"value"`
			DisplayName   string   `json:"displayName"`
			Description   string   `json:"description"`
			EffortLevels  []string `json:"supportedEffortLevels"`
			SupportsEffor bool     `json:"supportsEffort"`
		} `json:"models"`
	}
	if json.Unmarshal(res, &r) != nil {
		return nil
	}
	var out []wire.ModelInfo
	for _, m := range r.Models {
		if m.Value == "" {
			continue
		}
		out = append(out, wire.ModelInfo{ID: m.Value, Name: m.DisplayName, Description: m.Description, Efforts: m.EffortLevels, Default: m.Value == "default"})
	}
	return out
}

func (h *chatHost) cacheClaudeModels(res json.RawMessage) {
	saveModels(h.home, "claude", claudeModels(res))
}

// codexModels reads model/list's answer.
func codexModels(res json.RawMessage) []wire.ModelInfo {
	var r struct {
		Data []struct {
			Model         string `json:"model"`
			DisplayName   string `json:"displayName"`
			Description   string `json:"description"`
			Hidden        bool   `json:"hidden"`
			IsDefault     bool   `json:"isDefault"`
			DefaultEffort string `json:"defaultReasoningEffort"`
			Efforts       []struct {
				Effort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if json.Unmarshal(res, &r) != nil {
		return nil
	}
	var out []wire.ModelInfo
	for _, m := range r.Data {
		if m.Hidden || m.Model == "" {
			continue
		}
		mi := wire.ModelInfo{ID: m.Model, Name: m.DisplayName, Description: m.Description, DefaultEffort: m.DefaultEffort, Default: m.IsDefault}
		for _, e := range m.Efforts {
			mi.Efforts = append(mi.Efforts, e.Effort)
		}
		out = append(out, mi)
	}
	return out
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
	saveModels(h.home, "codex", codexModels(res))
}

// models is what an agent offers, asking it when nothing is known yet.
func (h *chatHost) models(agent string) (map[string]any, error) {
	if ms := loadModels(h.home, agent); len(ms) > 0 {
		return map[string]any{"models": ms}, nil
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
	return map[string]any{"models": loadModels(h.home, agent)}, nil
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

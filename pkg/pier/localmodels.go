package pier

// The models an agent offers, asked of its CLI on this machine — the same
// handshake a session's agent answers, which costs nothing — so a picker
// knows them, fast mode and efforts included, before any session has run.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/usepier/pier/pkg/pier/chat"
	"github.com/usepier/pier/pkg/pier/wire"
)

// localModels asks the agent's CLI here, if it's installed: its models
// and its slash commands.
func localModels(ctx context.Context, agent string) ([]wire.ModelInfo, []wire.AgentCommand, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch agent {
	case "claude":
		res, err := askLocal(ctx, []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json",
			"--verbose", "--no-session-persistence"},
			[]any{map[string]any{"type": "control_request", "request_id": "models", "request": map[string]any{"subtype": "initialize"}}},
			1, func(m map[string]json.RawMessage) (json.RawMessage, bool) {
				var r struct {
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
				}
				if string(m["type"]) != `"control_response"` || json.Unmarshal(m["response"], &r) != nil || r.RequestID != "models" {
					return nil, false
				}
				return r.Response, true
			})
		if err != nil {
			return nil, nil, err
		}
		return chat.ClaudeModels(res[0]), chat.ClaudeCommands(res[0]), nil
	case "codex":
		res, err := askLocal(ctx, []string{"codex", "app-server", "--listen", "stdio://"},
			[]any{
				map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "pier", "title": "pier", "version": "1"}}},
				map[string]any{"method": "initialized"},
				map[string]any{"id": 2, "method": "model/list", "params": map[string]any{"includeHidden": false}},
				map[string]any{"id": 3, "method": "skills/list", "params": map[string]any{}},
			},
			2, func(m map[string]json.RawMessage) (json.RawMessage, bool) {
				// The answers to model/list and skills/list, in the order
				// they come; which is which is in their shape.
				if id := string(m["id"]); id != "2" && id != "3" {
					return nil, false
				}
				return m["result"], m["result"] != nil
			})
		if err != nil {
			return nil, nil, err
		}
		var models []wire.ModelInfo
		var commands []wire.AgentCommand
		for _, r := range res {
			if ms := chat.CodexModels(r); len(ms) > 0 {
				models = ms
			} else {
				commands = chat.CodexCommands(r)
			}
		}
		return models, commands, nil
	}
	return nil, nil, fmt.Errorf("unknown agent %q", agent)
}

// askLocal runs argv, writes msgs to it a line each, and returns the first
// n messages match picks from what it writes back.
func askLocal(ctx context.Context, argv []string, msgs []any, n int, match func(map[string]json.RawMessage) (json.RawMessage, bool)) ([]json.RawMessage, error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("%s isn't installed here", argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	for _, m := range msgs {
		b, _ := json.Marshal(m)
		if _, err := in.Write(append(b, '\n')); err != nil {
			return nil, err
		}
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var got []json.RawMessage
	for sc.Scan() {
		var m map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if res, ok := match(m); ok {
			if got = append(got, res); len(got) == n {
				return got, nil
			}
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s didn't answer", argv[0])
	}
	return nil, errors.New(argv[0] + " exited without answering")
}

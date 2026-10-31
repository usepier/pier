package chat

import (
	"encoding/json"
	"testing"
)

// claude's commands come from its handshake, its internal ones left out.
func TestClaudeCommands(t *testing.T) {
	res := json.RawMessage(`{"commands":[{"name":"compact","description":"Clear history but keep a summary","argumentHint":"<instructions>"},
{"name":"__remote-workflow","description":"internal"},{"name":"deploy","description":"Ship it (project)","argumentHint":""}]}`)
	cs := ClaudeCommands(res)
	if len(cs) != 2 || cs[0].Name != "compact" || cs[0].Hint != "<instructions>" || cs[1].Name != "deploy" {
		t.Errorf("commands = %+v", cs)
	}
}

// codex's are its compact and the enabled skills it found, each once.
func TestCodexCommands(t *testing.T) {
	res := json.RawMessage(`{"data":[{"cwd":"/w","errors":[],"skills":[
{"name":"deploy","description":"Ship it","shortDescription":"Ship","path":"/s/deploy/SKILL.md","enabled":true,"scope":"user"},
{"name":"off","description":"x","path":"/s/off/SKILL.md","enabled":false,"scope":"user"}]},
{"cwd":"/w2","errors":[],"skills":[{"name":"deploy","description":"Ship it","path":"/s/deploy/SKILL.md","enabled":true,"scope":"user"}]}]}`)
	cs := CodexCommands(res)
	if len(cs) != 2 || cs[0].Name != CodexCompact || cs[1].Name != "deploy" || cs[1].Description != "Ship" || cs[1].Path != "/s/deploy/SKILL.md" {
		t.Errorf("commands = %+v", cs)
	}
}

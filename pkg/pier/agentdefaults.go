package pier

// What an agent runs when a chat names no model or effort: what its own
// config sets — sessions get a copy of it — else what its model list says.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/usepier/pier/pkg/pier/wire"
)

// agentDefaults reads the model and effort the agent's own config on this
// machine sets, if sessions get that config (manifest).
func agentDefaults(agent string, manifest []string) (model, effort string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	switch agent {
	case "claude":
		const rel = ".claude/settings.json"
		var s struct {
			Model  string `json:"model"`
			Effort string `json:"effortLevel"`
		}
		if b, err := os.ReadFile(filepath.Join(home, rel)); err == nil && inManifest(manifest, rel) && json.Unmarshal(b, &s) == nil {
			return s.Model, s.Effort
		}
	case "codex":
		const rel = ".codex/config.toml"
		var s struct {
			Model  string `toml:"model"`
			Effort string `toml:"model_reasoning_effort"`
		}
		if _, err := toml.DecodeFile(filepath.Join(home, rel), &s); err == nil && inManifest(manifest, rel) {
			return s.Model, s.Effort
		}
	}
	return "", ""
}

// inManifest reports whether sessions get the file at rel (under $HOME).
func inManifest(manifest []string, rel string) bool {
	return slices.ContainsFunc(manifest, func(p string) bool {
		p = strings.TrimSuffix(p, "/")
		return rel == p || strings.HasPrefix(rel, p+"/")
	})
}

// withAgentDefaults marks the model and effort an agent's config sets as
// what it runs by default: the model (by its alias or the model it
// resolves to; a 1M one with 1M on), and the effort on each model that has
// it.
func withAgentDefaults(ms []wire.ModelInfo, model, effort string) []wire.ModelInfo {
	if base, long := strings.CutSuffix(model, "[1m]"); base != "" {
		i := slices.IndexFunc(ms, func(m wire.ModelInfo) bool { return m.ID == base || m.Resolved == base })
		if i >= 0 {
			for j := range ms {
				ms[j].Default, ms[j].LongDefault = j == i, j == i && long && ms[j].Long
			}
		}
	}
	if effort != "" {
		for i, m := range ms {
			if slices.Contains(m.Efforts, effort) {
				ms[i].DefaultEffort = effort
			}
		}
	}
	return ms
}

package chat

// The models an agent offers, read from its own answers: claude's
// handshake (initialize), codex's model/list. A session's supervisor and
// pier on the laptop read them alike.

import (
	"encoding/json"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

// ClaudeModels reads the models of claude's initialize answer.
func ClaudeModels(res json.RawMessage) []wire.ModelInfo {
	var r struct {
		Models []struct {
			Value        string   `json:"value"`
			DisplayName  string   `json:"displayName"`
			Description  string   `json:"description"`
			EffortLevels []string `json:"supportedEffortLevels"`
			Fast         bool     `json:"supportsFastMode"`
			Resolved     string   `json:"resolvedModel"`
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
		out = append(out, wire.ModelInfo{ID: m.Value, Name: m.DisplayName, Description: m.Description, Efforts: m.EffortLevels, Default: m.Value == "default", Fast: m.Fast, Resolved: m.Resolved})
	}
	return out
}

// ClaudeCommands reads the slash commands of claude's initialize answer:
// its own, custom commands and skills.
func ClaudeCommands(res json.RawMessage) []wire.AgentCommand {
	var r struct {
		Commands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Hint        string `json:"argumentHint"`
		} `json:"commands"`
	}
	if json.Unmarshal(res, &r) != nil {
		return nil
	}
	var out []wire.AgentCommand
	for _, c := range r.Commands {
		// Internal ones start with "__".
		if c.Name == "" || strings.HasPrefix(c.Name, "__") {
			continue
		}
		out = append(out, wire.AgentCommand{Name: c.Name, Description: c.Description, Hint: c.Hint})
	}
	return out
}

// CodexCompact is codex's own command: compaction is a request of its own
// rather than a turn's text.
const CodexCompact = "compact"

// CodexCommands reads codex's skills/list answer: the skills a turn can
// be handed, after its own compact.
func CodexCommands(res json.RawMessage) []wire.AgentCommand {
	out := []wire.AgentCommand{{Name: CodexCompact, Description: "Summarize the conversation so far to free up context."}}
	var r struct {
		Data []struct {
			Skills []struct {
				Name             string `json:"name"`
				Description      string `json:"description"`
				ShortDescription string `json:"shortDescription"`
				Path             string `json:"path"`
				Enabled          bool   `json:"enabled"`
			} `json:"skills"`
		} `json:"data"`
	}
	if json.Unmarshal(res, &r) != nil {
		return out
	}
	seen := map[string]bool{}
	for _, d := range r.Data {
		for _, s := range d.Skills {
			if !s.Enabled || s.Name == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			desc := s.ShortDescription
			if desc == "" {
				desc = s.Description
			}
			out = append(out, wire.AgentCommand{Name: s.Name, Description: desc, Path: s.Path})
		}
	}
	return out
}

// CodexModels reads codex's model/list answer.
func CodexModels(res json.RawMessage) []wire.ModelInfo {
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
			Tiers []struct {
				ID string `json:"id"`
			} `json:"serviceTiers"`
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
		for _, t := range m.Tiers {
			mi.Fast = mi.Fast || t.ID == "fast"
		}
		out = append(out, mi)
	}
	return out
}

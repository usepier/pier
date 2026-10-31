package pier

import (
	"slices"
	"strings"
	"testing"

	"github.com/usepier/pier/pkg/pier/wire"
)

// Claude's list names a model by its family and keeps the version in the
// description; its "default" stands for one of the others.
func TestNamedModels(t *testing.T) {
	got := namedModels([]wire.ModelInfo{
		{ID: "default", Name: "Default (recommended)", Description: "Sonnet 5.5 · Efficient for routine tasks", Default: true},
		{ID: "sonnet", Name: "Sonnet", Description: "Sonnet 5.5 · Efficient for routine tasks"},
		{ID: "opus", Name: "Opus", Description: "Opus 5.5 · Best for everyday, complex tasks", Fast: true},
		{ID: "gpt-6.1-sol", Name: "GPT-6.1-Sol", Description: "Latest workhorse model."},
	})
	names := []string{}
	for _, m := range got {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "Sonnet 5.5,Opus 5.5,GPT-6.1-Sol" {
		t.Fatalf("names = %v", names)
	}
	if !got[0].Default || got[1].Default || got[0].Description != "Efficient for routine tasks" || !got[1].Fast {
		t.Errorf("models = %+v", got)
	}
}

// The default entry folds into the model it resolves to, under the name
// with its version; a 1M-context variant keeps that said.
func TestNamedModelsByResolvedModel(t *testing.T) {
	got := namedModels([]wire.ModelInfo{
		{ID: "default", Name: "Default (recommended)", Description: "Opus 5.5 with 1M context · Best for everyday, complex tasks", Resolved: "claude-opus-5-5[1m]", Fast: true},
		{ID: "opus[1m]", Name: "Opus (1M context)", Description: "Opus 5.5 with 1M context · Best for everyday, complex tasks", Resolved: "claude-opus-5-5[1m]", Fast: true},
		{ID: "haiku", Name: "Haiku", Description: "Haiku 4.5 · Fastest for quick answers", Resolved: "claude-haiku-4-5"},
	})
	if len(got) != 2 || got[0].Name != "Opus 5.5 (1M)" || !got[0].Default || got[1].Name != "Haiku 4.5" || got[1].Default {
		t.Errorf("models = %+v", got)
	}
	if ws := withUltracode([]wire.ModelInfo{{ID: "opus", Efforts: []string{"low", "xhigh", "max"}}, {ID: "haiku"}}); !slices.Contains(ws[0].Efforts, "ultracode") || ws[1].Efforts != nil {
		t.Errorf("ultracode = %+v", ws)
	}
}

// 1M context is a variant of a model, not a model: claude's "opus[1m]"
// row folds into Opus 5.5, with its 1M variant marked (and as the default
// when the default runs it).
func TestWithLong(t *testing.T) {
	got := withLong(namedModels([]wire.ModelInfo{
		{ID: "default", Name: "Default (recommended)", Description: "Opus 5.5 with 1M context · Best for everyday, complex tasks", Resolved: "claude-opus-5-5[1m]", Fast: true},
		{ID: "opus[1m]", Name: "Opus (1M context)", Description: "Opus 5.5 with 1M context · Best for everyday, complex tasks", Resolved: "claude-opus-5-5[1m]", Fast: true},
		{ID: "sonnet", Name: "Sonnet", Description: "Sonnet 5 · Efficient for routine tasks", Resolved: "claude-sonnet-5"},
	}))
	if len(got) != 2 {
		t.Fatalf("models = %+v", got)
	}
	o := got[0]
	if o.ID != "opus" || o.Name != "Opus 5.5" || !o.Long || !o.LongDefault || !o.Default || !o.Fast || got[1].Long || got[1].Default {
		t.Errorf("models = %+v", got)
	}
}

// What the agent's own config sets is what it runs by default: that model
// (by alias or what it resolves to), and that effort where a model has it.
func TestWithAgentDefaults(t *testing.T) {
	ms := func() []wire.ModelInfo {
		return []wire.ModelInfo{
			{ID: "opus", Resolved: "claude-opus-5-5", Long: true, LongDefault: true, Default: true, Efforts: []string{"high", "xhigh"}},
			{ID: "sonnet", Resolved: "claude-sonnet-5", Long: true, Efforts: []string{"high", "xhigh"}},
			{ID: "haiku", Resolved: "claude-haiku-4-5"},
		}
	}
	got := withAgentDefaults(ms(), "claude-sonnet-5[1m]", "xhigh")
	if got[0].Default || got[0].LongDefault || !got[1].Default || !got[1].LongDefault {
		t.Errorf("default model = %+v", got)
	}
	if got[0].DefaultEffort != "xhigh" || got[1].DefaultEffort != "xhigh" || got[2].DefaultEffort != "" {
		t.Errorf("default effort = %+v", got)
	}
	got = withAgentDefaults(ms(), "sonnet", "")
	if !got[1].Default || got[1].LongDefault {
		t.Errorf("alias = %+v", got)
	}
	// A model the list doesn't have leaves the agent's own default.
	if got = withAgentDefaults(ms(), "gone-1", ""); !got[0].Default {
		t.Errorf("unknown model = %+v", got)
	}
}

func TestInManifest(t *testing.T) {
	m := []string{".claude/settings.json", ".codex/"}
	if !inManifest(m, ".claude/settings.json") || !inManifest(m, ".codex/config.toml") || inManifest(m, ".claude/agents/x.md") {
		t.Error("inManifest")
	}
}

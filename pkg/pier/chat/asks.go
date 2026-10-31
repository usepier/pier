package chat

import (
	"encoding/json"
	"strings"

	"github.com/usepier/pier/pkg/pier/wire"
)

// What an agent asks of you through a tool: questions (AskUserQuestion)
// and plans to approve (ExitPlanMode).

// Asks reads a tool call's questions (with their title) or plan; all empty
// for any other tool.
func Asks(tool string, input json.RawMessage) (title string, questions []wire.Question, plan string) {
	in := inputMap(input)
	switch tool {
	case "AskUserQuestion":
		return str(in, "title"), questionsOf(in["questions"]), ""
	case "ExitPlanMode":
		return "", nil, str(in, "plan")
	}
	return "", nil, ""
}

func num(m map[string]any, k string) *float64 {
	if v, ok := m[k].(float64); ok {
		return &v
	}
	return nil
}

func questionsOf(v any) []wire.Question {
	list, _ := v.([]any)
	var out []wire.Question
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil || str(m, "question") == "" {
			continue
		}
		q := wire.Question{
			ID: str(m, "question"), Question: str(m, "question"), Header: str(m, "header"), Helper: str(m, "helper"),
			Kind: str(m, "kind"), Placeholder: str(m, "placeholder"), Unit: str(m, "unit"),
			Min: num(m, "min"), Max: num(m, "max"), Step: num(m, "step"), Default: num(m, "defaultValue"),
		}
		if q.Kind == "" {
			q.Kind = "choice"
		}
		q.MultiSelect, _ = m["multiSelect"].(bool)
		html := str(m, "previewFormat") == "html"
		opts, _ := m["options"].([]any)
		for _, o := range opts {
			om, _ := o.(map[string]any)
			if om == nil || str(om, "label") == "" {
				continue
			}
			q.Options = append(q.Options, wire.QuestionOption{
				Label: str(om, "label"), Description: str(om, "description"), Preview: str(om, "preview"), PreviewHTML: html,
			})
		}
		out = append(out, q)
	}
	return out
}

// answersOf reads a question tool's answers from its structured result.
func answersOf(sr map[string]any) map[string]string {
	a, _ := sr["answers"].(map[string]any)
	if len(a) == 0 {
		return nil
	}
	out := map[string]string{}
	for q, v := range a {
		if s, ok := v.(string); ok {
			out[q] = s
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "# "))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return oneLine(s)
}

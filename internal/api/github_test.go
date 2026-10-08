package api

import (
	"strings"
	"testing"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func TestPullRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", Repo: "shop", Branch: "fix", Origin: "github.com/acme/shop", State: pier.StateRunning}}})
	resp, _ := c.call("overview.get", map[string]any{})
	var o wire.Overview
	remarshal(t, resp["result"], &o)
	s := o.Sessions[0]
	if s.PR == nil || s.PR.Number != 7 || s.PR.Checks != "failing" || s.Attention != "merged_cleanup" {
		t.Errorf("session = %+v pr %+v", s, s.PR)
	}

	resp, _ = c.call("pr.merge", map[string]any{"sessionId": "i-1", "method": "squash", "expectedHeadOid": "old"})
	if errKind(resp) != "head_moved" {
		t.Errorf("merge past the reviewed head = %v", resp)
	}
	resp, _ = c.call("chat.compose", map[string]any{"sessionId": "i-1", "kind": "fixCI"})
	text, _ := resp["result"].(map[string]any)["text"].(string)
	if !strings.Contains(text, "PR #7") || !strings.Contains(text, "--- FAIL: TestLogin") {
		t.Errorf("fixCI prompt = %q", text)
	}
	resp, _ = c.call("chat.compose", map[string]any{"sessionId": "i-1", "kind": "addressComments"})
	text, _ = resp["result"].(map[string]any)["text"].(string)
	if !strings.Contains(text, "a.go:3\nrev: rename this") {
		t.Errorf("addressComments prompt = %q", text)
	}
	// The checkout still has uncommitted work: not removed.
	resp, _ = c.call("sessions.removeMerged", map[string]any{"id": "i-1"})
	if e, _ := resp["error"].(map[string]any); e == nil || !strings.Contains(e["message"].(string), "uncommitted") {
		t.Errorf("guarded remove = %v", resp)
	}
}

func TestAttentionOrder(t *testing.T) {
	waiting := pier.Session{State: pier.StateRunning, Agent: &pier.AgentStatus{State: "waiting"}}
	if a := attention(waiting, &wire.PRSummary{State: "open", Checks: "failing"}); a != "needs_input" {
		t.Errorf("an agent waiting comes first: %s", a)
	}
	ok := pier.Session{State: pier.StateRunning}
	cases := map[string]wire.PRSummary{
		"ci_failing":        {State: "open", Checks: "failing", Review: "approved"},
		"changes_requested": {State: "open", Checks: "passing", Review: "changes_requested"},
		"ready_to_merge":    {State: "open", Checks: "passing", Review: "approved", Mergeable: "clean"},
		"":                  {State: "open", Checks: "pending", Review: "approved", Mergeable: "clean"},
		"merged_cleanup":    {State: "merged"},
	}
	for want, pr := range cases {
		if got := attention(ok, &pr); got != want {
			t.Errorf("%+v = %q, want %q", pr, got, want)
		}
	}
}

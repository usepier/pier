package api

import (
	"testing"

	"github.com/usepier/pier/pkg/pier"
)

func TestEnvWatch(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", State: pier.StateRunning}}})
	resp, _ := c.call("env.watch", map[string]any{"id": "i-1"})
	snap := resp["result"].(map[string]any)["snapshot"].(map[string]any)
	ws := snap["windows"].([]any)
	cs := snap["containers"].([]any)
	if len(ws) != 1 || ws[0].(map[string]any)["role"] != "agent" || len(cs) != 1 || cs[0].(map[string]any)["name"] != "db" {
		t.Fatalf("env snapshot = %v", snap)
	}
	ev := c.next("sub.event")["params"].(map[string]any)["data"].(map[string]any)
	if p := ev["processes"].([]any)[0].(map[string]any); p["pid"] != float64(101) {
		t.Errorf("update = %v", ev)
	}
	resp, _ = c.call("containers.action", map[string]any{"sessionId": "i-1", "container": "c1", "action": "restart"})
	if resp["error"] != nil {
		t.Errorf("container action = %v", resp)
	}
}

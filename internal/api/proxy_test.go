package api

import (
	"testing"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func TestProxy(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}})
	resp, _ := c.call("proxy.start", map[string]any{})
	var st wire.ProxyState
	remarshal(t, resp["result"], &st)
	if !st.Running || len(st.Sessions) != 1 || st.Sessions[0].Host != "fix.pier" || st.Sessions[0].Ports[0] != 3000 {
		t.Fatalf("proxy = %+v", resp)
	}
	c.call("ports.forward", map[string]any{"sessionId": "i-1", "local": 8080, "remote": 3000})
	resp, _ = c.call("ports.list", map[string]any{})
	if l, _ := resp["result"].([]any); len(l) != 1 {
		t.Errorf("forwards = %v", resp)
	}
	resp, _ = c.call("proxy.stop", map[string]any{})
	if resp["error"] != nil {
		t.Errorf("stop = %v", resp)
	}
}

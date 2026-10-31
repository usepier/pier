package api

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier"
)

func termOutput(t *testing.T, c *client, id string, until func(string) bool) string {
	t.Helper()
	var got strings.Builder
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-c.msgs:
			p, _ := m["params"].(map[string]any)
			if m["method"] == "term.output" && p["termId"] == id {
				b, _ := base64.StdEncoding.DecodeString(p["data"].(string))
				got.Write(b)
				if until(got.String()) {
					return got.String()
				}
			}
		case <-deadline:
			t.Fatalf("terminal output so far: %q", got.String())
		}
	}
}

func TestTerminalRoundTrip(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", State: pier.StateRunning}}})
	resp, _ := c.call("term.open", map[string]any{"sessionId": "i-1", "cols": 80, "rows": 24})
	id := resp["result"].(map[string]any)["termId"].(string)
	termOutput(t, c, id, func(s string) bool { return strings.Contains(s, "ready") })
	c.send(map[string]any{"method": "term.input", "params": map[string]any{"termId": id, "data": base64.StdEncoding.EncodeToString([]byte("hi\r"))}})
	termOutput(t, c, id, func(s string) bool { return strings.Contains(s, "got:hi") })
	exit := c.next("term.exit")["params"].(map[string]any)
	if exit["termId"] != id || exit["code"] != float64(0) {
		t.Errorf("exit = %v", exit)
	}
}

// Output past the unacknowledged window waits for the client; acks let the
// rest through.
func TestTerminalFlowControl(t *testing.T) {
	f := &fake{termScript: "head -c 3000000 /dev/zero | tr '\\0' x", sessions: []pier.Session{{ID: "i-1", State: pier.StateRunning}}}
	c := start(t, f)
	resp, _ := c.call("term.open", map[string]any{"sessionId": "i-1", "cols": 80, "rows": 24})
	id := resp["result"].(map[string]any)["termId"].(string)
	received := 0
	quiet := time.After(1500 * time.Millisecond)
collect:
	for {
		select {
		case m := <-c.msgs:
			if p, _ := m["params"].(map[string]any); m["method"] == "term.output" && p["termId"] == id {
				b, _ := base64.StdEncoding.DecodeString(p["data"].(string))
				received += len(b)
			}
		case <-quiet:
			break collect
		}
	}
	if received > termWindow+termChunk || received == 0 {
		t.Fatalf("received %d bytes unacknowledged, window is %d", received, termWindow)
	}
	c.send(map[string]any{"method": "term.ack", "params": map[string]any{"termId": id, "bytes": received}})
	more := termOutput(t, c, id, func(s string) bool { return len(s) > 1000 })
	if len(more) == 0 {
		t.Error("an ack must let more output through")
	}
}

func TestWindows(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", State: pier.StateRunning}}})
	resp, _ := c.call("tmux.windows", map[string]any{"id": "i-1"})
	ws := resp["result"].([]any)
	if len(ws) != 2 || ws[0].(map[string]any)["role"] != "agent" {
		t.Errorf("windows = %v", ws)
	}
	resp, _ = c.call("tmux.newWindow", map[string]any{"sessionId": "i-1", "name": "server"})
	if w := resp["result"].(map[string]any); w["id"] != "@3" || w["name"] != "server" {
		t.Errorf("newWindow = %v", w)
	}
}

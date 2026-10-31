package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/pkg/pier"
)

func TestSetupLog(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", State: pier.StateRunning}}})
	resp, _ := c.call("logs.setup", map[string]any{"id": "i-1"})
	r := resp["result"].(map[string]any)
	if r["truncated"] != true || !strings.HasPrefix(r["text"].(string), "==> installing") {
		t.Errorf("logs.setup = %v", r)
	}
}

// The login that fixes an expired session runs on request, its output
// (where to go, what code to enter) narrated as progress.
func TestAuthLogin(t *testing.T) {
	f := &fake{listErr: &driver.LoginError{Login: []string{"x"}, Err: errors.New("expired")}}
	c := start(t, f)
	c.call("overview.get", nil) // the refresh fails with the expired login
	f.mu.Lock()
	f.listErr = nil
	f.mu.Unlock()
	resp, notes := c.call("auth.login", nil)
	if resp["error"] != nil {
		t.Fatalf("auth.login = %v", resp)
	}
	var lines []string
	for _, n := range notes {
		lines = append(lines, n["params"].(map[string]any)["event"].(map[string]any)["message"].(string))
	}
	got := strings.Join(lines, "|")
	if !strings.Contains(got, "Open https://device.example/verify") || !strings.Contains(got, "code: ABCD-1234") {
		t.Errorf("progress = %q", got)
	}
}

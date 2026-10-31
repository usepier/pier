package proxy

import (
	"strings"
	"testing"
)

// The boot task goes in with printf on one line (the administrator prompt
// runs the script as one): nothing in it may need quoting.
func TestBootTaskPrintsOnOneLine(t *testing.T) {
	task := bootTask()
	for _, bad := range []string{"'", "%", "\\\\"} {
		if strings.Contains(task, bad) {
			t.Errorf("the boot task holds %q", bad)
		}
	}
	if !strings.Contains(task, "<string>"+bootLabel+"</string>") || !strings.Contains(task, "ifconfig lo0 alias "+dnsAddr) {
		t.Errorf("task = %s", task)
	}
	if n := strings.Count(task, "ifconfig lo0 alias"); n != len(proxyIPs()) {
		t.Errorf("%d aliases, want %d", n, len(proxyIPs()))
	}
}

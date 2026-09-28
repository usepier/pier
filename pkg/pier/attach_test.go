package pier

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The session asks for web pages, never programs: only http(s) URLs open.
func TestOpenURLOnlyOpensWebPages(t *testing.T) {
	var opened []string
	old := openCommand
	openCommand = func(name string, args ...string) *exec.Cmd {
		opened = append(opened, args...)
		return exec.Command("true")
	}
	t.Cleanup(func() { openCommand = old })
	if err := openURL("https://github.com/login/oauth/authorize?client_id=x"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "/usr/bin/true", "https://", ""} {
		if openURL(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if len(opened) != 1 {
		t.Errorf("want exactly the one web page opened, got %v", opened)
	}
}

// A URL the session sends over the forwarded socket opens here, and the
// session hears "ok" back.
func TestOpenerRoundTrip(t *testing.T) {
	got := make(chan string, 1)
	old := openCommand
	openCommand = func(name string, args ...string) *exec.Cmd {
		got <- args[len(args)-1]
		return exec.Command("true")
	}
	t.Cleanup(func() { openCommand = old })
	sock, closeFn, err := listenOpener()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(c, "http://localhost:8123/callback?code=abc")
	reply, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if strings.TrimSpace(reply) != "ok" {
		t.Errorf("want ok, got %q", reply)
	}
	select {
	case u := <-got:
		if u != "http://localhost:8123/callback?code=abc" {
			t.Errorf("opened %q", u)
		}
	case <-time.After(2 * time.Second):
		t.Error("the URL never opened")
	}
}

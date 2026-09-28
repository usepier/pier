package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A session has no browser. Anything in it that opens a URL — an agent's
// OAuth login, `gh auth login`, a dev server's "open in browser" — goes
// through xdg-open or $BROWSER, both of which point here. While someone is
// attached, their attach connection forwards a unix socket back to their
// laptop, and the URL opens in their own browser; otherwise the URL is
// printed so it can be copied.

// openSocket is where an attach forwards the laptop's opener.
func openSocket(home string) string { return filepath.Join(home, ".pier", "open.sock") }

func openMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: xdg-open <url>")
		return 1
	}
	url := args[0]
	home, _ := os.UserHomeDir()
	if err := sendOpen(openSocket(home), url); err != nil {
		fmt.Fprintln(os.Stderr, "open this in your browser: "+url)
	}
	return 0
}

// sendOpen hands one URL to the laptop's opener and waits for its answer.
func sendOpen(sock, url string) error {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintln(c, url); err != nil {
		return err
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(reply) != "ok" {
		return fmt.Errorf("opener: %s", strings.TrimSpace(reply))
	}
	return nil
}

package main

// What agent programs run with: the user's environment as their shells
// get it, in the session's checkout.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// checkoutDir is the session's checkout (~/work/<repo>); home when there is
// none yet.
func checkoutDir(home string) string {
	if repo, _ := filepath.Glob(filepath.Join(home, "work", "*")); len(repo) > 0 {
		return repo[0]
	}
	return home
}

// agentEnv is the environment agent programs get: the supervisor's, the
// session's own (~/.config/pier/env: tokens, the user's variables), and
// what an interactive shell there would add.
func agentEnv(home string) []string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range readEnvFile(filepath.Join(home, ".config", "pier", "env")) {
		env[k] = v
	}
	env["HOME"] = home
	env["USER"], env["LOGNAME"] = filepath.Base(home), filepath.Base(home)
	env["SHELL"] = "/bin/bash"
	env["BROWSER"] = "/usr/local/bin/xdg-open"
	env["SSH_AUTH_SOCK"] = filepath.Join(home, ".ssh", "agent.sock")
	// Pinned programs don't update themselves.
	env["DISABLE_AUTOUPDATER"] = "1"
	path := []string{filepath.Join(home, ".local", "bin"), filepath.Join(pinDir(home, pins["rg"]))}
	if p := env["PATH"]; p != "" {
		path = append(path, p)
	} else {
		path = append(path, "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	env["PATH"] = strings.Join(path, ":")
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// readEnvFile reads KEY='value' lines (the way pier writes them).
func readEnvFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		k = strings.TrimPrefix(k, "export ")
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
		} else if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// tailBuffer keeps the end of what a program writes to stderr, and a copy
// in its log file (cut short when it grows past 1 MB).
type tailBuffer struct {
	mu   sync.Mutex
	buf  []byte
	path string
}

func newTailBuffer(path string) *tailBuffer { return &tailBuffer{path: path} }

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > 8<<10 {
		t.buf = t.buf[len(t.buf)-8<<10:]
	}
	if t.path != "" {
		if fi, err := os.Stat(t.path); err == nil && fi.Size() > 1<<20 {
			os.Truncate(t.path, 0)
		}
		if f, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			f.Write(b)
			f.Close()
		}
	}
	return len(b), nil
}

// tail is the last few lines written.
func (t *tailBuffer) tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(stripANSI(string(t.buf))), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func newOpID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newUUID is a random (v4) UUID.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// killGroup ends a program and everything it started.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	killTree(cmd.Process.Pid)
}

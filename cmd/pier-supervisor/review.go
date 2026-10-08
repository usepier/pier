package main

// Changes to the session's checkout that frontends ask for — saving a
// file, committing, pushing — run here rather than in the ssh channel that
// asked: a dropped connection can't leave a file half written or a push
// half done, and the op id makes a retry a no-op.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// workDir is a checkout's directory: ~/work/<repo>.
func workDir(repo string) (string, error) {
	if !repoName.MatchString(repo) || repo == "." || repo == ".." {
		return "", fmt.Errorf("bad repo %q", repo)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "work", repo), nil
}

// inside resolves a path relative to a checkout, refusing anything that
// leaves it — through .. or through a symlink.
func inside(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q isn't a path in the checkout", rel)
	}
	p := filepath.Join(dir, rel)
	if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the checkout", rel)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	// The deepest existing ancestor must still be in the checkout.
	for parent := filepath.Dir(p); ; parent = filepath.Dir(parent) {
		real, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
				return "", fmt.Errorf("%q leaves the checkout", rel)
			}
			break
		}
		if parent == dir || parent == "/" {
			return "", err
		}
	}
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%q is a symlink", rel)
	}
	return p, nil
}

func fileSha(p string) (string, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// writeOp saves a file of a checkout unless it changed since the frontend
// read it: {repo, path, data (base64), base_sha ("" for a new file)}.
func writeOp(raw json.RawMessage) (any, error) {
	var a struct {
		Repo, Path, Data string
		BaseSha          string `json:"base_sha"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	dir, err := workDir(a.Repo)
	if err != nil {
		return nil, err
	}
	p, err := inside(dir, a.Path)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(a.Data)
	if err != nil {
		return nil, err
	}
	cur, err := fileSha(p)
	if err != nil {
		return nil, err
	}
	if cur != a.BaseSha {
		return nil, fmt.Errorf("conflict: %s changed since it was read", a.Path)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	rnd := make([]byte, 4)
	rand.Read(rnd)
	tmp := p + ".pier-" + hex.EncodeToString(rnd)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	h := sha256.Sum256(data)
	return map[string]string{"sha": hex.EncodeToString(h[:])}, nil
}

// gitOp commits or pushes a checkout: {repo, action: commit|push, message}.
func gitOp(raw json.RawMessage) (any, error) {
	var a struct{ Repo, Action, Message string }
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	dir, err := workDir(a.Repo)
	if err != nil {
		return nil, err
	}
	run := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		// The session's credentials (GH_TOKEN for gh's git helper) live in
		// its env file, which the supervisor's own environment lacks.
		script := `set -a; . ~/.config/pier/env 2>/dev/null; set +a; exec "$@"`
		cmd := exec.CommandContext(ctx, "bash", append([]string{"-c", script, "bash", "git"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if ctx.Err() != nil {
				msg = "timed out"
			}
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return strings.TrimSpace(string(out)), nil
	}
	switch a.Action {
	case "commit":
		if strings.TrimSpace(a.Message) == "" {
			return nil, errors.New("a commit needs a message")
		}
		if _, err := run(time.Minute, "add", "-A"); err != nil {
			return nil, err
		}
		if _, err := run(5*time.Minute, "commit", "-m", a.Message); err != nil {
			return nil, err
		}
		head, err := run(10*time.Second, "rev-parse", "HEAD")
		return map[string]string{"head": head}, err
	case "push":
		_, err := run(5*time.Minute, "push", "-u", "origin", "HEAD")
		return nil, err
	}
	return nil, fmt.Errorf("unknown git action %q", a.Action)
}

func init() {
	ops["write"] = writeOp
	ops["git"] = gitOp
}

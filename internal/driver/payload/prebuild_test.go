package payload

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/internal/driver"
)

// The scrub is the only thing between a prebuild's secrets and an image
// anyone in the account can launch: it must delete every file the cargo
// delivered, and nothing setup built — that warmth is what the bake is for.
func TestScrubScriptRemovesCargoAndKeepsWarmth(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	repo := filepath.Join(home, "work", "myrepo")
	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "tracked.txt"), "clean\n")
	mustGit("init", "-q")
	mustGit("-c", "user.name=t", "-c", "user.email=t@t", "add", ".")
	mustGit("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")

	cargo := map[string]string{
		"home/.claude/settings.json": "{}",
		"home/.codex/auth.json":      "secret",
		"home/.claude.json":          "{}",
		"home/.config/pier/env":      "GH_TOKEN=secret",
		"repo/.env":                  "DB_PASSWORD=secret",
		"repo/it's quoted.env":       "x",
	}
	tarPath := filepath.Join(t.TempDir(), "pier-files.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for name, body := range cargo {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
		// Delivered where the bootstrap extracts it.
		if rel, ok := strings.CutPrefix(name, "home/"); ok {
			write(filepath.Join(home, rel), body)
		} else {
			write(filepath.Join(repo, strings.TrimPrefix(name, "repo/")), body)
		}
	}
	tw.Close()
	f.Close()

	// What setup built, and first-boot state.
	write(filepath.Join(repo, "node_modules", "dep", "index.js"), "warm")
	write(filepath.Join(repo, "tracked.txt"), "dirty edit\n")
	write(filepath.Join(home, ".cache", "pnpm", "store"), "warm")
	for _, p := range []string{".pier-setup.status", ".pier-setup.log", ".pier-bootstrapped", ".docker/config.json", ".git-credentials"} {
		write(filepath.Join(home, p), "x")
	}

	script, err := ScrubScript(tarPath, "myrepo")
	if err != nil {
		t.Fatal(err)
	}
	// tmux and sudo are stubbed: the scrub must never kill the tmux server or
	// touch docker on the machine running the tests.
	stubs := t.TempDir()
	log := filepath.Join(stubs, "calls")
	for _, name := range []string{"tmux", "sudo"} {
		write(filepath.Join(stubs, name), "#!/bin/sh\necho "+name+" \"$@\" >> "+log+"\n")
		os.Chmod(filepath.Join(stubs, name), 0o755)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+stubs+":"+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scrub: %v\n%s", err, out)
	}

	for name := range cargo {
		p := filepath.Join(home, strings.TrimPrefix(name, "home/"))
		if rel, ok := strings.CutPrefix(name, "repo/"); ok {
			p = filepath.Join(repo, rel)
		}
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the scrub", name)
		}
	}
	for _, p := range []string{".pier-setup.status", ".pier-setup.log", ".pier-bootstrapped", ".docker/config.json", ".git-credentials"} {
		if _, err := os.Stat(filepath.Join(home, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("~/%s survived the scrub", p)
		}
	}
	for _, p := range []string{filepath.Join(repo, "node_modules", "dep", "index.js"), filepath.Join(home, ".cache", "pnpm", "store")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("setup artifact %s was scrubbed: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "tracked.txt")); string(b) != "clean\n" {
		t.Errorf("tracked edits must be reset, got %q", b)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "tmux kill-server") {
		t.Error("the scrub must end the tmux server before imaging")
	}
	if !strings.Contains(string(calls), "sudo -n docker ps -aq") {
		t.Error("the scrub must remove containers (their config holds env_file values), non-interactively")
	}
}

func TestWaitSetup(t *testing.T) {
	old := setupPoll
	setupPoll = time.Millisecond
	t.Cleanup(func() { setupPoll = old })

	seq := func(answers ...string) func(context.Context, string) (string, error) {
		i := 0
		return func(_ context.Context, cmd string) (string, error) {
			if strings.HasPrefix(cmd, "tail") {
				return "boom", nil
			}
			a := answers[min(i, len(answers)-1)]
			i++
			return a, nil
		}
	}
	if err := WaitSetup(context.Background(), seq("none", "running", "0"), time.Minute); err != nil {
		t.Fatalf("a setup that finishes must pass: %v", err)
	}
	err := WaitSetup(context.Background(), seq("running", "2"), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failed setup must fail with its exit code and log tail, got %v", err)
	}
	if err := WaitSetup(context.Background(), seq("running"), 5*time.Millisecond); err == nil {
		t.Fatal("a setup that never finishes must time out")
	}
}

// A session launched from a prebuilt image must reuse the checkout — the
// artifacts around it are the point — rather than fail on `git init` or
// `git remote add` against a repo that already has them.
func TestRenderBootstrapReusesPrebuiltCheckout(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: "/tmp/myrepo", Branch: "feat"}
	b := renderBootstrap(spec, "origin", "abc123", "https://github.com/o/r")
	for _, want := range []string{
		`if [ -d .git ]; then prebuilt=1; else git init -q -b 'feat'; fi`,
		`git remote set-url origin 'https://github.com/o/r' 2>/dev/null || git remote add origin 'https://github.com/o/r'`,
		`if [ -n "$prebuilt" ]; then git checkout -qf -B 'feat' abc123; fi`,
		`git branch -qD '` + PrebuildBranch + `'`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("bootstrap missing %q", want)
		}
	}
	if BootstrapNote("") == BootstrapNote("ami-1") {
		t.Error("a baked create must not claim to wait on a stock image's cloud-init")
	}
}

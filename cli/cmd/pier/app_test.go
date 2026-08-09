package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

func TestAppInstanceFrom(t *testing.T) {
	created := time.Date(2026, time.August, 9, 10, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	got := appInstanceFrom(driver.Session{
		ID: "i-123", Name: "fix-login", Repo: "pier", Branch: "fix-login",
		State: driver.StateWorking, Created: created,
	})
	if got.ID != "i-123" || got.State != driver.StateWorking {
		t.Fatalf("appInstanceFrom() = %#v", got)
	}
	if got.CreatedAt != "2026-08-09T08:30:00Z" {
		t.Errorf("CreatedAt = %q, want UTC RFC3339", got.CreatedAt)
	}
}

func TestValidateTabID(t *testing.T) {
	for _, id := range []string{"@0", "@42"} {
		if err := validateTabID(id); err != nil {
			t.Errorf("validateTabID(%q): %v", id, err)
		}
	}
	for _, id := range []string{"0", "main", "@1; touch /tmp/nope", ""} {
		if err := validateTabID(id); err == nil {
			t.Errorf("validateTabID(%q) accepted an invalid id", id)
		}
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("it's safe")
	if got != `'it'\''s safe'` {
		t.Errorf("shellQuote() = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Error("test fixture unexpectedly contains a newline")
	}
}

func TestLikelyHTTPPort(t *testing.T) {
	if !likelyHTTPPort(3000) || !likelyHTTPPort(4200) || !likelyHTTPPort(8088) || likelyHTTPPort(5432) {
		t.Error("likelyHTTPPort classification changed")
	}
	if webScheme(8443) != "https" || webScheme(9000) != "http" {
		t.Error("web port schemes changed")
	}
}

func TestAppListeningProcesses(t *testing.T) {
	output := `LISTEN 0 511 *:4200 *:* users:(("node",pid=123,fd=20))
LISTEN 0 4096 0.0.0.0:5432 0.0.0.0:* users:(("docker-proxy",pid=456,fd=4))
LISTEN 0 128 [::]:8088 [::]:*`
	got := appListeningProcesses(output)
	if got[4200] != "node" || got[5432] != "docker-proxy" {
		t.Fatalf("process map = %#v", got)
	}
	if _, ok := got[8088]; ok {
		t.Fatalf("port without process should stay unnamed: %#v", got)
	}
}

func TestAppDockerPortProcesses(t *testing.T) {
	output := "api\t0.0.0.0:8088->8080/tcp, [::]:8088->8080/tcp\npostgres\t0.0.0.0:5432->5432/tcp\nminio\t0.0.0.0:9000-9001->9000-9001/tcp\n"
	got := appDockerPortProcesses(output)
	if got[8088] != "api" || got[5432] != "postgres" || got[9000] != "minio" || got[9001] != "minio" {
		t.Fatalf("docker port map = %#v", got)
	}
}

func TestAppValidPort(t *testing.T) {
	if got, err := appValidPort("4200"); err != nil || got != 4200 {
		t.Fatalf("valid port = %d, %v", got, err)
	}
	for _, value := range []string{"0", "65536", "nope"} {
		if _, err := appValidPort(value); err == nil {
			t.Errorf("invalid port %q accepted", value)
		}
	}
}

func TestAppAttachRemoteCommandTargetsTmuxWindow(t *testing.T) {
	got := appAttachRemoteCommand("@42")
	for _, want := range []string{
		`client_session="pier-app-$$"`,
		`window_index=$(tmux display-message -p -t '@42' '#{window_index}')`,
		`tmux new-session -d -t main -s "$client_session"`,
		`tmux select-window -t "$client_session:$window_index"`,
		`tmux attach-session -t "$client_session"`,
		`set-titles-string '#{pane_current_command}'`,
		`trap cleanup EXIT HUP INT TERM`,
		`ln -sf "$SSH_AUTH_SOCK" ~/.ssh/agent.sock`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("attach command missing %q: %s", want, got)
		}
	}
}

func TestAppTabNameAndIDValidation(t *testing.T) {
	if !appTabName.MatchString("claude-2") || appTabName.MatchString("bad tab") {
		t.Error("tab name validation changed")
	}
}

func TestAppCreateTabRecreatesMissingTmuxSession(t *testing.T) {
	got := appCreateTabRemoteCommand(
		[]string{"tmux", "new-window", "-t", "main", "--", "claude"},
		[]string{"tmux", "new-session", "-s", "main", "--", "claude"},
	)
	for _, want := range []string{
		"if tmux has-session -t main",
		"'tmux' 'new-window' '-t' 'main' '--' 'claude'",
		"else 'tmux' 'new-session' '-s' 'main' '--' 'claude'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("create command missing %q: %s", want, got)
		}
	}
}

func TestAppCloseTabIsIdempotent(t *testing.T) {
	got := appCloseTabRemoteCommand("@42")
	if got != "tmux kill-window -t '@42' 2>/dev/null || true" {
		t.Fatalf("appCloseTabRemoteCommand() = %q", got)
	}
}

func TestAppAbbreviateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := appAbbreviateHome(filepath.Join(home, "Documents", "pier")); got != "~/Documents/pier" {
		t.Errorf("appAbbreviateHome() = %q", got)
	}
}

func TestTmuxWindowFormatUsesRealUnitSeparators(t *testing.T) {
	format := appTmuxWindowFormat()
	if !strings.Contains(format, `\037`) {
		t.Fatalf("tmux format does not contain its serialized delimiter: %q", format)
	}
	if fields := strings.Split("@10"+appTmuxSeparator+"shell"+appTmuxSeparator+"1", appTmuxSeparator); len(fields) != 3 {
		t.Fatalf("unit-separated tmux row parsed into %d fields", len(fields))
	}
}

func TestCloudErrorKeepsOrdinaryErrors(t *testing.T) {
	code, message := appCloudError("list_failed", errors.New("network unavailable"))
	if code != "list_failed" || message != "network unavailable" {
		t.Fatalf("appCloudError() = %q, %q", code, message)
	}
}

func TestAppCollectProjectsFindsAndDeduplicatesGitRoots(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	projects := appCollectProjects([]string{root, nested, filepath.Join(root, "missing")})
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != resolvedRoot || projects[0].Name != filepath.Base(root) {
		t.Fatalf("appCollectProjects() = %#v", projects)
	}
}

func TestAppNormalizeBranchesPrefersRemoteAndDropsOriginHEAD(t *testing.T) {
	got := appNormalizeBranches([]string{"feature", "origin/main", "origin/HEAD", "main", "origin/main", ""})
	want := []string{"origin/main", "feature", "main"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("appNormalizeBranches() = %v, want %v", got, want)
	}
}

func TestCloudErrorRecognizesIncompleteSSOCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hash := "d033e22ae348aeb5660fc2140aec35850c4da997"
	cacheDir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, hash+".json"), []byte(`{"accessToken":"unfinished`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, message := appCloudError("list_failed", errors.New("aws: [ERROR]: '"+hash+"'"))
	if code != "aws_login_required" || !strings.Contains(message, "Sign in again") {
		t.Fatalf("appCloudError() = %q, %q", code, message)
	}
}

func TestQuarantineInvalidSSOCachesPreservesMalformedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(cacheDir, "valid.json")
	invalid := filepath.Join(cacheDir, "invalid.json")
	if err := os.WriteFile(valid, []byte(`{"accessToken":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte(`{"accessToken":"unfinished`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appQuarantineInvalidSSOCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("valid cache was moved: %v", err)
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatalf("invalid cache still exists: %v", err)
	}
	backups, err := filepath.Glob(invalid + ".corrupt-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("invalid cache backup = %v, %v", backups, err)
	}
}

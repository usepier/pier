package gcpgce

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
)

// gcloud crashes print multi-paragraph "run gcloud feedback" boilerplate;
// only the ERROR line may reach the TUI status line and CLI errors.
func TestGcloudErr(t *testing.T) {
	crash := "WARNING: something minor\nERROR: gcloud crashed (SSLError): HTTPSConnectionPool(host='compute.googleapis.com', port=443): Max retries exceeded\n\nIf you would like to report this issue, please run the following command:\n  gcloud feedback\n\nTo check gcloud for common problems, please run the following command:\n  gcloud info --run-diagnostics\n"
	if got := gcloudErr(crash); got != "ERROR: gcloud crashed (SSLError): HTTPSConnectionPool(host='compute.googleapis.com', port=443): Max retries exceeded" {
		t.Errorf("gcloudErr(crash) = %q", got)
	}
	// No ERROR line: fall back to the first non-empty line.
	if got := gcloudErr("\nsome failure\ndetail\n"); got != "some failure" {
		t.Errorf("gcloudErr(no ERROR) = %q", got)
	}
	// Expired Workspace sessions must surface the remedy, not the
	// non-interactive-prompt plumbing detail.
	want := "gcloud auth has expired — run `gcloud auth login`, then retry"
	reauth := "ERROR: (gcloud.compute.instances.list) There was a problem refreshing your current auth tokens: Reauthentication failed. cannot prompt during non-interactive execution.\n"
	if got := gcloudErr(reauth); got != want {
		t.Errorf("gcloudErr(reauth) = %q", got)
	}
	revoked := "ERROR: There was a problem refreshing your current auth tokens: invalid_grant: Token has been expired or revoked.\n"
	if got := gcloudErr(revoked); got != want {
		t.Errorf("gcloudErr(revoked) = %q", got)
	}
}

// The instance name is the session ID and a GCE resource name: it must stay
// under 63 chars, never end in "-" before the hash, and differ per principal
// so two devs on one project can hold the same session name.
func TestInstanceName(t *testing.T) {
	a := instanceName("fix-auth", "kerem@example.com")
	b := instanceName("fix-auth", "other@example.com")
	if a == b {
		t.Fatalf("same name for different principals: %s", a)
	}
	if a != instanceName("fix-auth", "kerem@example.com") {
		t.Fatal("instance name is not deterministic")
	}
	if !strings.HasPrefix(a, "pier-fix-auth-") {
		t.Fatalf("unexpected shape: %s", a)
	}

	long := instanceName(strings.Repeat("a", 80)+"-b", "kerem@example.com")
	if len(long) > 63 {
		t.Fatalf("name over GCE's 63-char limit: %d %s", len(long), long)
	}
	if strings.Contains(long, "--") {
		t.Fatalf("truncation left a dangling hyphen: %s", long)
	}
}

func TestLabelValue(t *testing.T) {
	if got := labelValue("Kerem@Example.COM"); got != "kerem-example-com" {
		t.Fatalf("labelValue fold: got %q", got)
	}
	if got := labelValue("--x--"); got != "x" {
		t.Fatalf("labelValue trim: got %q", got)
	}
	if got := labelValue(strings.Repeat("a", 80)); len(got) > 63 {
		t.Fatalf("labelValue over 63 chars: %d", len(got))
	}
}

func TestArchAndRegion(t *testing.T) {
	for typ, want := range map[string]string{
		"e2-medium": "amd64", "t2a-standard-2": "arm64", "c4a-standard-4": "arm64", "n2-standard-8": "amd64",
	} {
		if got := archOf(typ); got != want {
			t.Fatalf("archOf(%s) = %s, want %s", typ, got, want)
		}
	}
	if got := regionOf("europe-west3-a"); got != "europe-west3" {
		t.Fatalf("regionOf: got %q", got)
	}
}

func TestAttachCommandFallsBackForUnknownTerminal(t *testing.T) {
	d := &Driver{Project: "p", Zone: "z", StateDir: t.TempDir()}
	cmd, err := d.AttachCommand(context.Background(), "pier-x-abc123")
	if err != nil {
		t.Fatal(err)
	}
	remote := cmd.Args[len(cmd.Args)-1]
	if !strings.Contains(remote, `infocmp "$TERM"`) || !strings.Contains(remote, "export TERM=xterm-256color") {
		t.Errorf("attach must fall back when the VM lacks the client's terminfo entry, got %q", remote)
	}
}

func TestMachinesCatalog(t *testing.T) {
	amd64Catalog := Machines("e2-medium")
	if len(amd64Catalog) == 0 {
		t.Fatal("expected non-empty amd64 catalog")
	}
	for _, m := range amd64Catalog {
		if archOf(m.Type) != "amd64" {
			t.Errorf("expected amd64 architecture for type %s, got %s", m.Type, archOf(m.Type))
		}
	}

	arm64Catalog := Machines("t2a-standard-2")
	if len(arm64Catalog) == 0 {
		t.Fatal("expected non-empty arm64 catalog")
	}
	for _, m := range arm64Catalog {
		if archOf(m.Type) != "arm64" {
			t.Errorf("expected arm64 architecture for type %s, got %s", m.Type, archOf(m.Type))
		}
	}

	if amd64Catalog[0].Type == arm64Catalog[0].Type {
		t.Fatalf("catalogs for amd64 and arm64 should be separate")
	}
}

// --- Hermetic Lifecycle Tests ---

func fakeExecCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", name}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func fakeExecCommand(name string, args ...string) *exec.Cmd {
	return fakeExecCommandContext(context.Background(), name, args...)
}

func init() {
	// Set package variables for testing
	execCommand = fakeExecCommand
	execCommandContext = fakeExecCommandContext
}

type mockResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func setupTestDriver(t *testing.T, responses map[string]mockResponse) (*Driver, string) {
	logFile := filepath.Join(t.TempDir(), "cmd.log")
	t.Setenv("FAKE_CMD_LOG", logFile)

	// Ensure gcloud auth list doesn't fail
	if responses == nil {
		responses = make(map[string]mockResponse)
	}
	responses["auth list"] = mockResponse{Stdout: "testuser@example.com\n"}

	b, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	mapFile := filepath.Join(t.TempDir(), "gcloud_map.json")
	os.WriteFile(mapFile, b, 0644)
	t.Setenv("FAKE_GCLOUD_MAP", mapFile)

	d := &Driver{
		Project:     "test-project",
		Zone:        "us-central1-a",
		MachineType: "e2-medium",
		DiskGiB:     40,
		StateDir:    t.TempDir(),
		SupervisorBin: func(arch string) ([]byte, error) {
			return []byte("supervisor"), nil
		},
	}
	return d, logFile
}

func readCmdLog(t *testing.T, logFile string) []string {
	b, err := os.ReadFile(logFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var cmds []string
	for _, l := range lines {
		cmds = append(cmds, strings.ReplaceAll(l, "\x00", " "))
	}
	return cmds
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args[3:]
	cmd := args[0]

	if logFile := os.Getenv("FAKE_CMD_LOG"); logFile != "" {
		f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		fmt.Fprintf(f, "%s\n", strings.Join(args, "\x00"))
		f.Close()
	}

	switch cmd {
	case "gcloud":
		if mapFile := os.Getenv("FAKE_GCLOUD_MAP"); mapFile != "" {
			b, _ := os.ReadFile(mapFile)
			var responses map[string]mockResponse
			json.Unmarshal(b, &responses)

			argsStr := strings.Join(args[1:], " ")
			for prefix, res := range responses {
				if strings.Contains(argsStr, prefix) {
					if res.ExitCode != 0 {
						os.Stderr.WriteString(res.Stderr)
						os.Exit(res.ExitCode)
					}
					os.Stdout.WriteString(res.Stdout)
					return
				}
			}
		}
	case "ssh-keygen":
		if len(args) > 8 && args[1] == "-t" {
			keyPath := args[8]
			os.WriteFile(keyPath+".pub", []byte("fake-pub-key"), 0644)
		}
	case "ssh", "scp":
		// succeed silently
	}
}

func setupDummyRepo(t *testing.T) string {
	dir := t.TempDir()
	// Use the real os/exec to initialize a dummy repo so payload.Build succeeds.
	exec.Command("git", "init", "-b", "main", dir).Run()
	configName := exec.Command("git", "config", "user.name", "Test User")
	configName.Dir = dir
	configName.Run()
	configEmail := exec.Command("git", "config", "user.email", "test@example.com")
	configEmail.Dir = dir
	configEmail.Run()
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	cmd.Run()
	return dir
}

func TestList(t *testing.T) {
	// A mix of states
	listJSON := `[
		{"name": "pier-s1-111111", "status": "PROVISIONING", "labels": {"pier-user": "testuser-example-com"}, "metadata": {"items": [{"key": "pier-user", "value": "testuser@example.com"}, {"key": "pier-session", "value": "s1"}]}},
		{"name": "pier-s2-222222", "status": "RUNNING", "labels": {"pier-user": "testuser-example-com", "pier-ready": "1"}, "metadata": {"items": [{"key": "pier-user", "value": "testuser@example.com"}, {"key": "pier-session", "value": "s2"}]}},
		{"name": "pier-s3-333333", "status": "RUNNING", "labels": {"pier-user": "testuser-example-com", "pier-deleting": "1"}, "metadata": {"items": [{"key": "pier-user", "value": "testuser@example.com"}, {"key": "pier-session", "value": "s3"}]}},
		{"name": "pier-s4-444444", "status": "TERMINATED", "labels": {"pier-user": "testuser-example-com"}, "metadata": {"items": [{"key": "pier-user", "value": "testuser@example.com"}, {"key": "pier-session", "value": "s4"}]}}
	]`

	d, logFile := setupTestDriver(t, map[string]mockResponse{
		"compute instances list": {Stdout: listJSON},
	})

	sessions, err := d.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cmds := readCmdLog(t, logFile)
	foundList := false
	for _, c := range cmds {
		if strings.Contains(c, "compute instances list") {
			foundList = true
			if !strings.Contains(c, "labels.pier-managed=1") || !strings.Contains(c, "labels.pier-user=testuser-example-com") {
				t.Errorf("list filter missing expected labels: %s", c)
			}
		}
	}
	if !foundList {
		t.Fatal("list command not found in log")
	}

	if len(sessions) != 4 {
		t.Fatalf("expected 4 sessions, got %d", len(sessions))
	}

	var states []driver.State
	for _, s := range sessions {
		states = append(states, s.State)
	}
	// order is sorted by session Name; since we didn't populate all names, they sort by ID (fallback).
	// s1 = creating, s2 = running, s3 = deleting, s4 = parked
	if states[0] != driver.StateCreating {
		t.Errorf("s1 should be creating, got %s", states[0])
	}
	if states[1] != driver.StateRunning {
		t.Errorf("s2 should be running, got %s", states[1])
	}
	if states[2] != driver.StateDeleting {
		t.Errorf("s3 should be deleting, got %s", states[2])
	}
	if states[3] != driver.StateParked {
		t.Errorf("s4 should be parked, got %s", states[3])
	}
}

func TestCreate(t *testing.T) {
	d, logFile := setupTestDriver(t, map[string]mockResponse{
		"compute instances create":     {Stdout: "pier-my-session-abcdef"},
		"compute instances add-labels": {Stdout: ""}, // for ready label
	})

	repo := setupDummyRepo(t)
	spec := driver.CreateSpec{
		Name:   "my-session",
		Repo:   repo,
		Branch: "main",
	}

	sess, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}

	if sess.State != driver.StateRunning {
		t.Errorf("expected running state, got %s", sess.State)
	}

	cmds := readCmdLog(t, logFile)
	foundCreate := false
	foundAddLabels := false
	for _, c := range cmds {
		if strings.Contains(c, "compute instances create") {
			foundCreate = true
			if !strings.Contains(c, "--no-service-account") || !strings.Contains(c, "--no-scopes") {
				t.Errorf("create missing secure defaults: %s", c)
			}
			if !strings.Contains(c, "--labels pier-managed=1,pier-user=testuser-example-com") {
				t.Errorf("create missing labels: %s", c)
			}
		}
		if strings.Contains(c, "add-labels") && strings.Contains(c, "pier-ready=1") {
			foundAddLabels = true
		}
	}
	if !foundCreate {
		t.Error("create command not run")
	}
	if !foundAddLabels {
		t.Error("ready label not added")
	}
}

func TestCreateFailureQuota(t *testing.T) {
	d, _ := setupTestDriver(t, map[string]mockResponse{
		"compute instances create": {Stderr: "ERROR: ... QUOTA_EXCEEDED", ExitCode: 1},
	})

	repo := setupDummyRepo(t)
	_, err := d.Create(context.Background(), driver.CreateSpec{
		Name:   "fail-session",
		Repo:   repo,
		Branch: "main",
	})
	if err == nil || !strings.Contains(err.Error(), "CPU quota exceeded") {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestCreateFailureCleanup(t *testing.T) {
	d, logFile := setupTestDriver(t, map[string]mockResponse{
		"compute instances create":     {Stdout: "pier-fail-session-1234"},
		"compute instances add-labels": {Stderr: "Simulated failure adding ready label", ExitCode: 1},
		"compute instances delete":     {Stdout: ""},
	})

	repo := setupDummyRepo(t)
	_, err := d.Create(context.Background(), driver.CreateSpec{
		Name:   "fail-session",
		Repo:   repo,
		Branch: "main",
	})
	if err == nil {
		t.Fatal("expected create to fail")
	}

	cmds := readCmdLog(t, logFile)
	foundDelete := false
	for _, c := range cmds {
		if strings.Contains(c, "compute instances delete") {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Errorf("expected cleanup (delete) after create failure, log: %v", cmds)
	}
}

func TestLifecycle(t *testing.T) {
	d, logFile := setupTestDriver(t, map[string]mockResponse{
		"compute instances stop":             {Stdout: ""},
		"compute instances start":            {Stdout: ""},
		"compute instances delete":           {Stdout: ""},
		"compute instances describe":         {Stdout: "RUNNING e2-medium"},
		"compute instances set-machine-type": {Stdout: ""},
	})

	id := "pier-session-123"

	// Park
	if err := d.Park(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	// Resume
	if err := d.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	// Resize (from e2-medium to e2-standard-4)
	if err := d.Resize(context.Background(), id, "e2-standard-4"); err != nil {
		t.Fatal(err)
	}

	// Cross-arch resize (should fail early)
	err := d.Resize(context.Background(), id, "t2a-standard-2")
	if err == nil || !strings.Contains(err.Error(), "cannot resize across architectures") {
		t.Fatalf("expected cross-arch error, got %v", err)
	}

	// Destroy
	if err := d.Destroy(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	cmds := readCmdLog(t, logFile)
	actions := strings.Join(cmds, "\n")

	if !strings.Contains(actions, "instances stop "+id+" --zone "+d.Zone+" --async") {
		t.Errorf("park missing --async stop, got: %s", actions)
	}

	// Resize should have stopped, set-machine-type, and started
	if !strings.Contains(actions, "set-machine-type "+id+" --zone "+d.Zone+" --machine-type e2-standard-4") {
		t.Errorf("resize missing set-machine-type, got: %s", actions)
	}

	// Destroy should add deleting label before deleting
	if !strings.Contains(actions, "add-labels "+id+" --zone "+d.Zone+" --labels pier-deleting=1") {
		t.Errorf("destroy missing deleting label, got: %s", actions)
	}
	if !strings.Contains(actions, "instances delete "+id+" --zone "+d.Zone) {
		t.Errorf("destroy missing delete, got: %s", actions)
	}
}

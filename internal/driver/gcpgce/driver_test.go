package gcpgce

import (
	"context"
	"strings"
	"testing"
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

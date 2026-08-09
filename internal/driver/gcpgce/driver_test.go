package gcpgce

import (
	"strings"
	"testing"
)

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

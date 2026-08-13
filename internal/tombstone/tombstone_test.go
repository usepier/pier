package tombstone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteListDismiss(t *testing.T) {
	dir := t.TempDir()
	if got := List(dir); len(got) != 0 {
		t.Fatalf("empty state dir must list nothing, got %v", got)
	}
	if err := Write(dir, Record{Name: "kur-3817", Repo: "flb", Reason: "scp: Connection closed"}); err != nil {
		t.Fatal(err)
	}
	got := List(dir)
	if len(got) != 1 || got[0].Name != "kur-3817" || got[0].Repo != "flb" {
		t.Fatalf("want the record back, got %+v", got)
	}
	// Write stamps When so a record without one still ages out eventually.
	if got[0].When.IsZero() {
		t.Error("Write must stamp When")
	}
	if err := Dismiss(dir, "kur-3817"); err != nil {
		t.Fatal(err)
	}
	if got := List(dir); len(got) != 0 {
		t.Fatalf("dismissed record must be gone, got %v", got)
	}
	// The caller wanted it gone and it is — absent is not an error.
	if err := Dismiss(dir, "kur-3817"); err != nil {
		t.Errorf("dismissing an absent record must succeed, got %v", err)
	}
}

// Session names are branch names, so they contain slashes; a record must not
// escape the graveyard directory or collide with a sibling.
func TestWriteHandlesSlashedNames(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Record{Name: "feat/login", Reason: "boom"}); err != nil {
		t.Fatal(err)
	}
	got := List(dir)
	if len(got) != 1 || got[0].Name != "feat/login" {
		t.Fatalf("want the slashed name back verbatim, got %+v", got)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "failed"))
	if len(ents) != 1 || strings.Contains(ents[0].Name(), "/") {
		t.Errorf("the file name must be flat, got %v", ents)
	}
	if err := Dismiss(dir, "feat/login"); err != nil || len(List(dir)) != 0 {
		t.Errorf("a slashed name must dismiss by the same key, err=%v", err)
	}
}

func TestListPrunesExpiredAndSortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	Write(dir, Record{Name: "old", Reason: "x", When: time.Now().Add(-retention - time.Hour)})
	Write(dir, Record{Name: "recent", Reason: "x", When: time.Now().Add(-time.Hour)})
	Write(dir, Record{Name: "newest", Reason: "x", When: time.Now()})

	got := List(dir)
	if len(got) != 2 {
		t.Fatalf("want the expired record pruned, got %+v", got)
	}
	if got[0].Name != "newest" || got[1].Name != "recent" {
		t.Errorf("want newest first, got %s then %s", got[0].Name, got[1].Name)
	}
	// Pruning is a delete, not a filter — the file must be gone from disk.
	if _, err := os.Stat(file(dir, "old")); !os.IsNotExist(err) {
		t.Error("an expired record must be removed from disk")
	}
}

// `pier ls` must survive anything in the graveyard: a corrupt tombstone is
// worth losing, the session list is not.
func TestListSkipsUnreadableRecords(t *testing.T) {
	dir := t.TempDir()
	Write(dir, Record{Name: "good", Reason: "x"})
	g := filepath.Join(dir, "failed")
	os.WriteFile(filepath.Join(g, "broken.json"), []byte("{not json"), 0o600)
	os.WriteFile(filepath.Join(g, "nameless.json"), []byte(`{"reason":"x"}`), 0o600)
	os.WriteFile(filepath.Join(g, "notes.txt"), []byte("ignored"), 0o600)

	got := List(dir)
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("want only the readable record, got %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	for in, want := range map[string]string{
		"pier: scp failed\nscp: Connection closed": "scp failed",
		"  bootstrap: exit 1  ":                    "bootstrap: exit 1",
		"":                                         "",
	} {
		if got := Summarize(in); got != want {
			t.Errorf("Summarize(%q) = %q, want %q", in, got, want)
		}
	}
	long := Summarize(strings.Repeat("é", 200))
	if n := len([]rune(long)); n != 72 {
		t.Errorf("a long reason must truncate to 72 runes, got %d", n)
	}
	if !strings.Contains(long, "…") {
		t.Errorf("a truncated summary must be marked, got %q", long)
	}
}

// The incident this was built for: the cause sits at the end of the line,
// behind a long temp path. Truncating the tail would keep the path and throw
// away the only part that explains anything.
func TestSummarizeKeepsTheCauseNotThePath(t *testing.T) {
	got := Summarize("pier: scp /var/folders/r2/yxgt1yxd3pvcy5ydh9y0863r0000gn/T/" +
		"pier-create-2086200156/pier-supervisor -> i-01f4b1c5a08c6863a: " +
		"ssh: connect to host 3.78.221.163 port 22: Network is unreachable")
	if !strings.Contains(got, "Network is unreachable") {
		t.Errorf("the cause must survive truncation, got %q", got)
	}
	if !strings.HasPrefix(got, "scp ") {
		t.Errorf("the operation must survive too, got %q", got)
	}
	if n := len([]rune(got)); n != 72 {
		t.Errorf("want 72 runes, got %d (%q)", n, got)
	}
}

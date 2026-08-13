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

// Branch names may contain both "/" and "-", so feat/login and feat-login are
// two different sessions. Folding one to the other would let each silently
// overwrite the other's failure — and dismissing one would delete the other.
func TestDistinctNamesThatFoldTogetherKeepSeparateRecords(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Record{Name: "feat/login", Reason: "slashed"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, Record{Name: "feat-login", Reason: "dashed"}); err != nil {
		t.Fatal(err)
	}
	if got := List(dir); len(got) != 2 {
		t.Fatalf("want both records, got %d: %+v", len(got), got)
	}

	// Dismissing one must leave the other completely alone.
	if err := Dismiss(dir, "feat/login"); err != nil {
		t.Fatal(err)
	}
	got := List(dir)
	if len(got) != 1 {
		t.Fatalf("want exactly one survivor, got %+v", got)
	}
	if got[0].Name != "feat-login" || got[0].Reason != "dashed" {
		t.Errorf("dismissed the wrong record: %+v", got[0])
	}
}

// Re-failing the same session replaces its row rather than stacking a second
// one, including when the existing file was written under an older scheme.
func TestWriteReplacesAnyExistingRecordForTheName(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "failed"), 0o700); err != nil {
		t.Fatal(err)
	}
	// What the old slash-folding scheme would have left for feat/login. Only
	// escaped names differ between the schemes; an unescaped one already
	// lands on its old path, so there is nothing to clean up there.
	legacy := filepath.Join(d, "failed", "feat-login.json")
	if err := os.WriteFile(legacy, []byte(`{"name":"feat/login","reason":"legacy","when":"`+
		time.Now().Format(time.RFC3339)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := List(d); len(got) != 1 || got[0].Name != "feat/login" {
		t.Fatalf("precondition: want the legacy record readable, got %+v", got)
	}

	if err := Write(d, Record{Name: "feat/login", Reason: "current"}); err != nil {
		t.Fatal(err)
	}
	got := List(d)
	if len(got) != 1 {
		t.Fatalf("one session must mean one row, got %+v", got)
	}
	if got[0].Reason != "current" {
		t.Errorf("want the newest reason, got %q", got[0].Reason)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the record written under the old scheme must be cleaned up")
	}
}

// The old scheme's collision is still on disk after an upgrade: feat/login's
// record can be sitting at exactly the path feat-login now writes to. Writing
// feat-login must not silently destroy the other session's failure.
func TestWriteRelocatesADifferentSessionSquattingItsPath(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "failed"), 0o700); err != nil {
		t.Fatal(err)
	}
	squatted := filepath.Join(d, "failed", "feat-login.json") // FileStem("feat-login")
	if err := os.WriteFile(squatted, []byte(`{"name":"feat/login","reason":"the slashed one","when":"`+
		time.Now().Format(time.RFC3339)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Write(d, Record{Name: "feat-login", Reason: "the dashed one"}); err != nil {
		t.Fatal(err)
	}

	byName := map[string]string{}
	for _, r := range List(d) {
		byName[r.Name] = r.Reason
	}
	if len(byName) != 2 {
		t.Fatalf("both sessions must keep a record, got %+v", byName)
	}
	if byName["feat/login"] != "the slashed one" {
		t.Errorf("the squatting record must be relocated, not destroyed: %+v", byName)
	}
	if byName["feat-login"] != "the dashed one" {
		t.Errorf("the new record must land: %+v", byName)
	}
}

// Relocation must never fall back to destroying the record it displaces, even
// when the obvious destination is itself taken. Records are found by the name
// inside them, so any free path will do.
func TestWriteNeverDestroysADisplacedRecord(t *testing.T) {
	d := t.TempDir()
	g := filepath.Join(d, "failed")
	if err := os.MkdirAll(g, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Format(time.RFC3339)
	write := func(file, name, reason string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g, file),
			[]byte(`{"name":"`+name+`","reason":"`+reason+`","when":"`+stamp+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// feat/login squats the path feat-login now wants, and its own canonical
	// path is already occupied by a third session that legitimately owns it.
	write("feat-login.json", "feat/login", "displaced")
	write("feat%2Flogin.json", "feat%2Flogin", "third session")

	if err := Write(d, Record{Name: "feat-login", Reason: "incoming"}); err != nil {
		t.Fatal(err)
	}

	byName := map[string]string{}
	for _, r := range List(d) {
		byName[r.Name] = r.Reason
	}
	if len(byName) != 3 {
		t.Fatalf("all three records must survive, got %+v", byName)
	}
	if byName["feat/login"] != "displaced" {
		t.Errorf("the displaced record was lost: %+v", byName)
	}
	if byName["feat%2Flogin"] != "third session" {
		t.Errorf("the third session must be untouched: %+v", byName)
	}
	if byName["feat-login"] != "incoming" {
		t.Errorf("the new record must land: %+v", byName)
	}
	// And it must still be dismissable from wherever it landed.
	if err := Dismiss(d, "feat/login"); err != nil {
		t.Fatal(err)
	}
	for _, r := range List(d) {
		if r.Name == "feat/login" {
			t.Error("a relocated record must still dismiss")
		}
	}
}

// FileStem keys both the create log and the failed-create record, so any two
// distinct names must produce distinct stems — and a name that needs no
// escaping must survive verbatim, since these file names are shown to users.
func TestFileStemIsInjectiveAndReadable(t *testing.T) {
	for _, verbatim := range []string{"kur-3814", "feat-login", "v1.2_x", "a"} {
		if got := FileStem(verbatim); got != verbatim {
			t.Errorf("FileStem(%q) = %q, want it unchanged", verbatim, got)
		}
	}

	names := []string{
		"feat/login", "feat-login", "feat%2Flogin", "feat%252Flogin",
		"a/b", "a-b", "a%2Fb", "", "..", "../../etc/passwd",
		strings.Repeat("x/", 200), strings.Repeat("x/", 199) + "y/",
	}
	seen := map[string]string{}
	for _, n := range names {
		s := FileStem(n)
		if prev, dup := seen[s]; dup {
			t.Errorf("%q and %q both key to %q", prev, n, s)
		}
		seen[s] = n
		if strings.Contains(s, "/") {
			t.Errorf("FileStem(%q) = %q must not contain a separator", n, s)
		}
		if len(s) > 120 {
			t.Errorf("FileStem(%q) is %d bytes, too long for a file name", n, len(s))
		}
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

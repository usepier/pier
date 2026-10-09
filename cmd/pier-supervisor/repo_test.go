package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier/gitview"
)

func query(t *testing.T, name string, args any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := marshalled(queries[name](raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// edited is a checkout with a commit on the branch and work on top: a
// modified file, a new one, an untracked one and a rename.
func edited(t *testing.T) string {
	dir := checkout(t)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("a\nb\nc\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "old")
	gitIn(t, dir, "update-ref", "refs/pier/base", "HEAD")
	os.WriteFile(filepath.Join(dir, "README"), []byte("shop\nmore\n"), 0o644)
	gitIn(t, dir, "commit", "-qam", "readme")
	gitIn(t, dir, "mv", "old.txt", "new.txt")
	os.WriteFile(filepath.Join(dir, "added.go"), []byte("package x\n"), 0o644)
	gitIn(t, dir, "add", "added.go")
	os.WriteFile(filepath.Join(dir, "loose.txt"), []byte("1\n2\n"), 0o644)
	return dir
}

func TestRepoChanges(t *testing.T) {
	edited(t)
	var c gitview.Changes
	json.Unmarshal(query(t, "repo.changes", nil), &c)
	byPath := map[string]gitview.File{}
	for _, f := range c.Files {
		byPath[f.Path] = f
	}
	if f := byPath["README"]; f.Status != gitview.Modified || f.Added != 1 || f.Uncommitted {
		t.Errorf("README = %+v", f)
	}
	if f := byPath["new.txt"]; f.Status != gitview.Renamed || f.OldPath != "old.txt" || !f.Uncommitted {
		t.Errorf("new.txt = %+v", f)
	}
	if f := byPath["added.go"]; f.Status != gitview.Added || f.Added != 1 {
		t.Errorf("added.go = %+v", f)
	}
	if f := byPath["loose.txt"]; f.Status != gitview.Untracked || f.Added != 2 {
		t.Errorf("loose.txt = %+v", f)
	}
	if c.Branch.Head != "main" || c.BaseOid == "" {
		t.Errorf("branch %+v base %q", c.Branch, c.BaseOid)
	}
}

func TestRepoChangesAgainstOrigin(t *testing.T) {
	dir := checkout(t)
	// The branch came off origin/main, which moved on since: the base is
	// where they part, not origin's tip.
	start := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
	gitIn(t, dir, "config", "pier.base", "main")
	os.WriteFile(filepath.Join(dir, "theirs"), []byte("x\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "theirs")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, dir, "reset", "-q", "--hard", start)
	var c gitview.Changes
	json.Unmarshal(query(t, "repo.changes", nil), &c)
	if c.BaseRef != "origin/main" || c.BaseOid != start || len(c.Files) != 0 {
		t.Errorf("changes = %+v", c)
	}
}

func TestRepoDiffAndFiles(t *testing.T) {
	dir := edited(t)
	var d struct {
		Patch     string
		Truncated bool
	}
	json.Unmarshal(query(t, "repo.diff", map[string]any{"path": "README", "status": "modified"}), &d)
	if !strings.Contains(d.Patch, "+more") {
		t.Errorf("README diff = %q", d.Patch)
	}
	json.Unmarshal(query(t, "repo.diff", map[string]any{"path": "loose.txt", "status": "untracked"}), &d)
	if !strings.Contains(d.Patch, "+1\n+2") {
		t.Errorf("untracked diff = %q", d.Patch)
	}
	json.Unmarshal(query(t, "repo.diff", map[string]any{"path": "new.txt", "oldPath": "old.txt", "status": "renamed"}), &d)
	if !strings.Contains(d.Patch, "rename from old.txt") {
		t.Errorf("rename diff = %q", d.Patch)
	}
	// More context: the whole file around the change.
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a\nb\nc\nd\n"), 0o644)
	json.Unmarshal(query(t, "repo.diff", map[string]any{"path": "new.txt", "oldPath": "old.txt", "status": "renamed", "context": 100}), &d)
	if !strings.Contains(d.Patch, " a\n b\n c\n+d") {
		t.Errorf("full diff = %q", d.Patch)
	}

	var ds struct {
		Patches map[string]string
		Skipped int
	}
	json.Unmarshal(query(t, "repo.diffs", map[string]any{"files": []map[string]any{{"path": "README"}, {"path": "../x"}}}), &ds)
	if !strings.Contains(ds.Patches["README"], "+more") || ds.Skipped != 1 {
		t.Errorf("diffs = %+v", ds)
	}

	var base, work fileContent
	json.Unmarshal(query(t, "repo.file", map[string]any{"path": "README", "at": "base"}), &base)
	json.Unmarshal(query(t, "repo.file", map[string]any{"path": "README", "at": "work"}), &work)
	if b, _ := base64.StdEncoding.DecodeString(base.Data); string(b) != "shop\n" {
		t.Errorf("base = %q", b)
	}
	if b, _ := base64.StdEncoding.DecodeString(work.Data); string(b) != "shop\nmore\n" || work.Sha != sha("shop\nmore\n") {
		t.Errorf("work = %q %s", b, work.Sha)
	}
	var gone fileContent
	json.Unmarshal(query(t, "repo.file", map[string]any{"path": "loose.txt", "at": "base"}), &gone)
	if !gone.Missing {
		t.Errorf("an untracked file at the base = %+v", gone)
	}
	os.WriteFile(filepath.Join(dir, "bin"), []byte{0, 1, 2}, 0o644)
	var bin fileContent
	json.Unmarshal(query(t, "repo.file", map[string]any{"path": "bin", "at": "work"}), &bin)
	if !bin.Binary {
		t.Errorf("binary = %+v", bin)
	}
	if _, err := queries["repo.file"](json.RawMessage(`{"path":"../etc/passwd","at":"work"}`)); errCode(err) != codeOutside {
		t.Errorf("outside = %v", err)
	}

	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "ignored"), 0o755)
	os.WriteFile(filepath.Join(dir, "ignored", "x"), nil, 0o644)
	var l struct{ Paths []string }
	json.Unmarshal(query(t, "repo.list", nil), &l)
	got := strings.Join(l.Paths, " ")
	if got != ".gitignore README added.go bin loose.txt new.txt" {
		t.Errorf("list = %s", got)
	}
}

func TestStreamSearch(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("no ripgrep")
	}
	dir := checkout(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Hello() {}\nvar x = hello\n"), 0o644)
	_, conn, sc := dialHub(t)
	next(t, conn, sc, "hello")

	conn.Write([]byte(`{"op":"search","id":"s1","args":{"query":"hello"}}` + "\n"))
	var all []searchMatch
	for {
		m := next(t, conn, sc, "search")
		var d struct {
			Matches []searchMatch
			Done    bool
		}
		json.Unmarshal(m.Data, &d)
		if m.ID != "s1" || m.Err != "" {
			t.Fatalf("search = %+v", m)
		}
		all = append(all, d.Matches...)
		if d.Done {
			break
		}
	}
	if len(all) != 2 || all[0].Path != "a.go" || all[0].Line != 1 || all[0].Col != 6 || all[1].Col != 9 {
		t.Errorf("matches = %+v", all)
	}

	conn.Write([]byte(`{"op":"search","id":"s2","args":{"query":"Hello","caseSensitive":true}}` + "\n"))
	m := next(t, conn, sc, "search")
	if !strings.Contains(string(m.Data), `"line":1`) || strings.Contains(string(m.Data), `"line":2`) {
		t.Errorf("case sensitive = %s", m.Data)
	}

	conn.Write([]byte(`{"op":"search","id":"s3","args":{"query":"(","regex":true}}` + "\n"))
	if m := next(t, conn, sc, "search"); m.Err == "" {
		t.Errorf("a bad pattern = %+v", m)
	}
}

func TestGitTopic(t *testing.T) {
	dir := checkout(t)
	defer func(d time.Duration) { pollEvery = d }(pollEvery)
	pollEvery = 100 * time.Millisecond
	h, conn, sc := dialHub(t)
	go h.watchGit()
	next(t, conn, sc, "hello")
	conn.Write([]byte(`{"op":"subscribe","topics":["git"]}` + "\n"))
	state := func() gitState {
		var st gitState
		json.Unmarshal(next(t, conn, sc, "git").Data, &st)
		return st
	}
	if st := state(); st.Changes == nil || len(st.Changes.Files) != 0 {
		t.Fatalf("first = %+v", st)
	}
	os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644)
	st := state()
	if st.Changes == nil || len(st.Changes.Files) != 1 || st.Changes.Files[0].Path != "README" {
		t.Fatalf("after an edit = %+v", st)
	}
	gitIn(t, dir, "commit", "-qam", "edit")
	if st := state(); len(st.Changes.Files) != 1 || st.Changes.Files[0].Uncommitted {
		t.Errorf("after a commit = %+v", st.Changes)
	}
	// A second follower gets the current state at once.
	c2, err := netDial()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	sc2 := newScanner(c2)
	next(t, c2, sc2, "hello")
	c2.Write([]byte(`{"op":"subscribe","topics":["git"]}` + "\n"))
	var st2 gitState
	json.Unmarshal(next(t, c2, sc2, "git").Data, &st2)
	if st2.Changes == nil || len(st2.Changes.Files) != 1 {
		t.Errorf("second follower = %+v", st2)
	}
}

func netDial() (net.Conn, error) { return net.Dial("unix", sockPath) }

func newScanner(c net.Conn) *bufio.Scanner { return bufio.NewScanner(c) }

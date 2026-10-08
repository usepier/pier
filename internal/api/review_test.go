package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func TestReview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", Repo: "shop", State: pier.StateRunning}}})
	resp, _ := c.call("git.changes", map[string]any{"id": "i-1"})
	var ch wire.Changes
	remarshal(t, resp["result"], &ch)
	if !ch.Live || ch.Branch != "fix" || ch.Uncommitted != 1 || len(ch.Files) != 1 || ch.Files[0].Added != 2 {
		t.Errorf("changes = %+v", resp)
	}
	resp, _ = c.call("files.read", map[string]any{"sessionId": "i-1", "path": "logo.png"})
	var fc wire.FileContent
	remarshal(t, resp["result"], &fc)
	if !fc.Binary || fc.Text != "" {
		t.Errorf("a binary file reads as binary: %+v", fc)
	}
	resp, _ = c.call("files.write", map[string]any{"sessionId": "i-1", "opId": "o1", "path": "a.go", "text": "x", "baseSha": "stale"})
	if kind := errKind(resp); kind != "conflict" {
		t.Errorf("stale write = %v", resp)
	}
	resp, _ = c.call("files.search", map[string]any{"sessionId": "i-1", "query": "hello"})
	var sr wire.SearchResult
	remarshal(t, resp["result"], &sr)
	if len(sr.Matches) != 1 || sr.Matches[0].Line != 3 {
		t.Errorf("search = %+v", resp)
	}
}

// A parked session answers from what this machine last read.
func TestReviewParked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := pier.MirrorDir("i-2")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "changes.json"), []byte(`{"Branch":{"Head":"old"},"Files":[{"Path":"b.go","Status":"added","Added":4}]}`), 0o600)
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-2", Name: "old", State: pier.StateParked}}})
	resp, _ := c.call("git.changes", map[string]any{"id": "i-2"})
	var ch wire.Changes
	remarshal(t, resp["result"], &ch)
	if ch.Live || ch.AsOf == nil || ch.Branch != "old" || len(ch.Files) != 1 {
		t.Errorf("parked changes = %+v", resp)
	}
	resp, _ = c.call("git.diff", map[string]any{"sessionId": "i-2", "path": "b.go"})
	if errKind(resp) != "parked" {
		t.Errorf("an uncached diff of a parked session = %v", resp)
	}
}

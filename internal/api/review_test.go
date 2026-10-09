package api

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/gitview"
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
	resp, _ = c.call("files.read", map[string]any{"sessionId": "i-1", "path": "a.go", "at": "base"})
	remarshal(t, resp["result"], &fc)
	if fc.Text != "package old\n" {
		t.Errorf("at base = %+v", resp)
	}
}

// Every running session's checkout is followed, watched or not, so a
// session that parks shows what it had; pool members and parked sessions
// aren't.
func TestKeepsEveryRunningSession(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{sessions: []pier.Session{
		{ID: "i-1", Name: "feat", State: pier.StateRunning},
		{ID: "i-2", Name: "old", State: pier.StateParked},
		{ID: "i-3", Name: "pool-x", State: pier.StateRunning, PoolGen: "g"},
	}}
	c := start(t, f)
	c.call("overview.watch", nil)
	following := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		var ids []string
		for id, n := range f.following {
			if n > 0 {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return strings.Join(ids, " ")
	}
	waitFor(t, func() bool { return following() == "i-1" }, func() string { return "following " + following() })
	f.mu.Lock()
	f.sessions[0].State = pier.StateParked
	f.mu.Unlock()
	c.send(map[string]any{"method": "client.activity", "params": map[string]any{"focused": true}})
	waitFor(t, func() bool { return following() == "" }, func() string { return "still following " + following() })
}

func waitFor(t *testing.T, ok func() bool, why func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(why())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSearchWatch(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}})
	resp, _ := c.call("search.watch", map[string]any{"sessionId": "i-1", "query": "hello"})
	var snap wire.WatchResult[wire.SearchResult]
	remarshal(t, resp["result"], &snap)
	if len(snap.Snapshot.Matches) != 0 || snap.Snapshot.Done {
		t.Errorf("snapshot = %+v", resp)
	}
	var got []wire.SearchMatch
	for {
		var ev wire.SearchResult
		remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &ev)
		got = append(got, ev.Matches...)
		if ev.Done {
			break
		}
	}
	if len(got) != 2 || got[0].Path != "a.go" || got[1].Path != "b.go" {
		t.Errorf("matches = %+v", got)
	}
	c.call("search.watch", map[string]any{"sessionId": "i-1", "query": "(", "regex": true})
	var ev wire.SearchResult
	remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &ev)
	if !ev.Done || ev.Error == nil || ev.Error.Message != "regex parse error" {
		t.Errorf("a bad pattern = %+v", ev)
	}
}

func TestGitWatch(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}}
	first := gitview.Changes{Branch: gitview.Branch{Head: "fix"}, Files: []gitview.File{{Path: "a.go", Status: gitview.Modified, Added: 1}}}
	second := gitview.Changes{Branch: gitview.Branch{Head: "fix"}, Files: []gitview.File{{Path: "a.go", Status: gitview.Modified, Added: 2}}}
	f.gitStates = []pier.GitState{{Changes: &first, Stamps: map[string]string{"a.go": "s1"}}, {Changes: &second, Stamps: map[string]string{"a.go": "s2"}}}
	c := start(t, f)
	resp, _ := c.call("git.watch", map[string]any{"id": "i-1"})
	var snap wire.WatchResult[wire.Changes]
	remarshal(t, resp["result"], &snap)
	ch := snap.Snapshot
	if !ch.Live || len(ch.Files) != 1 {
		t.Fatalf("snapshot = %+v", resp)
	}
	// The snapshot is whichever push came last before it; the second
	// arrives as an event when the first was the snapshot.
	if ch.Files[0].Stamp == "s1" {
		remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &ch)
	}
	if ch.Files[0].Added != 2 || ch.Files[0].Stamp != "s2" {
		t.Errorf("latest = %+v", ch)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.kept)
		last := pier.GitState{}
		if n > 0 {
			last = f.kept[n-1]
		}
		f.mu.Unlock()
		if n > 0 && last.Stamps["a.go"] == "s2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the newest state wasn't kept (%d kept)", n)
		}
		time.Sleep(20 * time.Millisecond)
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
	// A watch of it answers with the kept copy too.
	resp, _ = c.call("git.watch", map[string]any{"id": "i-2"})
	var snap wire.WatchResult[wire.Changes]
	remarshal(t, resp["result"], &snap)
	if snap.Snapshot.Live || snap.Snapshot.AsOf == nil || len(snap.Snapshot.Files) != 1 {
		t.Errorf("parked watch = %+v", resp)
	}
	resp, _ = c.call("files.list", map[string]any{"sessionId": "i-2"})
	if errKind(resp) != "parked" {
		t.Errorf("an unkept list = %v", resp)
	}
	os.WriteFile(filepath.Join(dir, "files.json"), []byte(`{"paths":["b.go"]}`), 0o600)
	resp, _ = c.call("files.list", map[string]any{"sessionId": "i-2"})
	var fl wire.FileList
	remarshal(t, resp["result"], &fl)
	if len(fl.Paths) != 1 || fl.AsOf == nil {
		t.Errorf("kept list = %+v", resp)
	}
}

func TestTeardownNarrates(t *testing.T) {
	c := start(t, &fake{})
	resp, notes := c.call("account.teardown", nil)
	if resp["error"] != nil {
		t.Fatalf("teardown = %v", resp)
	}
	var msgs []string
	for _, n := range notes {
		if n["method"] == "$/progress" {
			msgs = append(msgs, n["params"].(map[string]any)["event"].(map[string]any)["message"].(string))
		}
	}
	if strings.Join(msgs, "|") != "removing pooled sessions|draining the pool of shop|the account is clean" {
		t.Errorf("progress = %v", msgs)
	}
}

// A watched session that parks goes over to its kept snapshot at once,
// not when its connection notices.
func TestGitWatchFollowsAPark(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}}
	live := gitview.Changes{Files: []gitview.File{{Path: "a.go", Status: gitview.Modified, Added: 1}}}
	f.gitStates = []pier.GitState{{Changes: &live, Stamps: map[string]string{"a.go": "s1"}}}
	c := start(t, f)
	c.call("overview.watch", nil)
	resp, _ := c.call("git.watch", map[string]any{"id": "i-1"})
	var snap wire.WatchResult[wire.Changes]
	remarshal(t, resp["result"], &snap)
	if !snap.Snapshot.Live {
		t.Fatalf("snapshot = %+v", snap.Snapshot)
	}
	dir := pier.MirrorDir("i-1")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "changes.json"), []byte(`{"Files":[{"Path":"a.go","Status":"modified","Added":1}]}`), 0o600)
	f.mu.Lock()
	f.sessions[0].State = pier.StateParked
	f.mu.Unlock()
	c.send(map[string]any{"method": "client.activity", "params": map[string]any{"focused": true}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := c.next("sub.event")
		p := ev["params"].(map[string]any)
		if p["topic"] == "git" {
			var ch wire.Changes
			remarshal(t, p["data"], &ch)
			if !ch.Live && ch.AsOf != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the watch never went over to the kept snapshot")
		}
	}
}

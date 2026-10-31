package pier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/pkg/pier/gitview"
)

// opDriver answers a session's supervisor ops (`pier-supervisor op
// '<json>'`) from handlers, and counts what was asked.
type opDriver struct {
	driver.Driver
	mu    sync.Mutex
	ops   map[string]func(args json.RawMessage) (any, error)
	asked map[string][]json.RawMessage
}

func (d *opDriver) Exec(_ context.Context, _ string, script string) (string, error) {
	quoted, ok := strings.CutPrefix(script, "pier-supervisor op ")
	if !ok {
		return "", fmt.Errorf("unexpected script %q", script)
	}
	var req struct {
		Op   string          `json:"op"`
		Args json.RawMessage `json:"args"`
	}
	raw := strings.ReplaceAll(strings.Trim(quoted, "'"), `'\''`, "'")
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.asked[req.Op] = append(d.asked[req.Op], req.Args)
	f := d.ops[req.Op]
	d.mu.Unlock()
	if f == nil {
		return "", fmt.Errorf("ssh i-1: unknown op %q", req.Op)
	}
	v, err := f(req.Args)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(v)
	return string(b), nil
}

func (d *opDriver) count(op string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.asked[op])
}

func newOpDriver() *opDriver {
	return &opDriver{ops: map[string]func(json.RawMessage) (any, error){}, asked: map[string][]json.RawMessage{}}
}

func TestOpErrorCodes(t *testing.T) {
	d := newOpDriver()
	d.ops["write"] = func(json.RawMessage) (any, error) {
		return nil, errors.New("ssh i-1: a.txt changed since it was read\npier-op-code: conflict")
	}
	d.ops["repo.changes"] = func(json.RawMessage) (any, error) {
		return nil, errors.New("ssh i-1: the session has no checkout yet\npier-op-code: no_checkout")
	}
	c := &Client{drv: d}
	s := Session{ID: "i-1", State: StateRunning}
	_, err := c.WriteFile(context.Background(), s, "w1", "a.txt", []byte("x"), "old")
	if !errors.Is(err, ErrConflict) || strings.Contains(err.Error(), "pier-op-code") {
		t.Errorf("write = %v", err)
	}
	var oe *OpError
	if _, err := c.Changes(context.Background(), s); !errors.Is(err, ErrNoCheckout) || !errors.As(err, &oe) || oe.Code != "no_checkout" {
		t.Errorf("changes = %v", err)
	}
}

// What a parked session shows is kept as the checkout changes, reading
// again only what changed.
func TestKeepSnapshot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	d := newOpDriver()
	d.ops["repo.diffs"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Files []struct{ Path string } }
		json.Unmarshal(raw, &a)
		out := map[string]string{}
		for _, f := range a.Files {
			out[f.Path] = "patch of " + f.Path
		}
		return map[string]any{"patches": out}, nil
	}
	d.ops["repo.files"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Paths []string }
		json.Unmarshal(raw, &a)
		out := map[string]any{}
		for _, p := range a.Paths {
			data := "content of " + p
			out[p] = map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(data)), "size": len(data), "sha": "s-" + p}
		}
		return map[string]any{"files": out}, nil
	}
	d.ops["repo.list"] = func(json.RawMessage) (any, error) {
		return map[string]any{"paths": []string{"a.go", "b.go", "c.go"}}, nil
	}
	c := &Client{drv: d}
	s := Session{ID: "i-1", State: StateRunning}
	ctx := context.Background()
	state := func(stamps map[string]string, files ...gitview.File) GitState {
		return GitState{Changes: &gitview.Changes{Files: files}, Stamps: stamps}
	}
	a := gitview.File{Path: "a.go", Status: gitview.Modified, Added: 1, Uncommitted: true}
	b := gitview.File{Path: "b.go", Status: gitview.Added, Added: 3}
	gone := gitview.File{Path: "gone.go", Status: gitview.Deleted, Removed: 2}

	if err := c.KeepSnapshot(ctx, s, state(map[string]string{"a.go": "1", "b.go": "1"}, a, b, gone)); err != nil {
		t.Fatal(err)
	}
	if p, _, ok := CachedDiff("i-1", "gone.go"); !ok || p != "patch of gone.go" {
		t.Errorf("deleted file's diff = %q", p)
	}
	if fc, _, ok := CachedFile("i-1", "a.go"); !ok || string(fc.Data) != "content of a.go" {
		t.Errorf("a.go kept = %+v", fc)
	}
	if _, _, ok := CachedFile("i-1", "gone.go"); ok {
		t.Error("a deleted file's content was asked for")
	}
	if paths, _, _, ok := CachedFiles("i-1"); !ok || len(paths) != 3 {
		t.Errorf("file list = %v", paths)
	}

	// a.go edited again, same counts: only it is read again; the list
	// stays (no file came or went).
	if err := c.KeepSnapshot(ctx, s, state(map[string]string{"a.go": "2", "b.go": "1"}, a, b, gone)); err != nil {
		t.Fatal(err)
	}
	if d.count("repo.diffs") != 2 || d.count("repo.list") != 1 {
		t.Errorf("asked diffs %d times, list %d", d.count("repo.diffs"), d.count("repo.list"))
	}
	var second struct{ Files []struct{ Path string } }
	json.Unmarshal(d.asked["repo.diffs"][1], &second)
	if len(second.Files) != 1 || second.Files[0].Path != "a.go" {
		t.Errorf("read again = %+v", second)
	}

	// b.go committed away: its copies go, and the list is read again.
	if err := c.KeepSnapshot(ctx, s, state(map[string]string{"a.go": "2"}, a, gone)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := CachedDiff("i-1", "b.go"); ok {
		t.Error("b.go's diff outlived its change")
	}
	if _, _, ok := CachedFile("i-1", "b.go"); ok {
		t.Error("b.go's content outlived its change")
	}
	if d.count("repo.diffs") != 2 || d.count("repo.list") != 2 {
		t.Errorf("asked diffs %d times, list %d", d.count("repo.diffs"), d.count("repo.list"))
	}
	if ch, _, ok := CachedChanges("i-1"); !ok || len(ch.Files) != 2 {
		t.Errorf("changes kept = %+v", ch)
	}
}

func TestParseGitState(t *testing.T) {
	g, err := ParseGitState(json.RawMessage(`{"changes":{"Files":[{"Path":"a","Status":"modified"}]},"stamps":{"a":"x"},"at":"2026-10-09T10:00:00Z"}`))
	if err != nil || g.Changes == nil || len(g.Changes.Files) != 1 || g.Stamps["a"] != "x" || g.Err != nil {
		t.Errorf("state = %+v %v", g, err)
	}
	g, _ = ParseGitState(json.RawMessage(`{"error":"the session has no checkout yet","code":"no_checkout"}`))
	if !errors.Is(g.Err, ErrNoCheckout) || g.Changes != nil {
		t.Errorf("no checkout = %+v", g)
	}
}

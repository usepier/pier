package pier

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/gitview"
)

// Reviewing a session's work from the laptop: what changed against the
// branch's base, each file's diff, the files themselves, and search. The
// session's supervisor reads its checkout (wherever it is) and pushes the
// changes as they happen; changes to it (a saved file, a commit, a push)
// run there too, so a dropped connection can't leave one half done and a
// retried id is a no-op. What was read last is kept on this machine, for
// when the session is parked.

// ErrConflict: the file changed in the session since it was read.
var ErrConflict = errors.New("the file changed in the session since it was read")

// ErrNoCheckout: the session has no checkout (yet).
var ErrNoCheckout = errors.New("the session has no checkout yet")

// OpError is a supervisor op that failed, with the code it gave.
type OpError struct {
	Code    string
	Message string
}

func (e *OpError) Error() string { return e.Message }

// Is lets errors.Is find the sentinel a code stands for.
func (e *OpError) Is(target error) bool {
	switch e.Code {
	case "conflict":
		return target == ErrConflict
	case "no_checkout":
		return target == ErrNoCheckout
	}
	return false
}

var opCodeLine = regexp.MustCompile(`\n?pier-op-code: ([a-z_]+)`)

// opError reads the code a failed op gave (on a line of its own).
func opError(err error) error {
	if err == nil {
		return nil
	}
	m := opCodeLine.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	return &OpError{Code: m[1], Message: strings.TrimSpace(opCodeLine.ReplaceAllString(err.Error(), ""))}
}

// query asks a session's supervisor about its checkout and decodes the
// answer into out.
func (c *Client) query(ctx context.Context, s Session, name string, args, out any) error {
	raw, err := c.Op(ctx, s, name, args)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// Changes reads how the session's checkout differs from its base. A copy
// is kept on this machine for when the session is parked (CachedChanges).
func (c *Client) Changes(ctx context.Context, s Session) (gitview.Changes, error) {
	var ch gitview.Changes
	if err := c.query(ctx, s, "repo.changes", nil, &ch); err != nil {
		return gitview.Changes{}, err
	}
	cacheJSON(s.ID, "changes.json", ch)
	return ch, nil
}

// CachedChanges is the last Changes this machine read for a session, and
// when.
func CachedChanges(sessionID string) (gitview.Changes, time.Time, bool) {
	var ch gitview.Changes
	at, ok := readCachedJSON(sessionID, "changes.json", &ch)
	return ch, at, ok
}

// GitState is what a session's supervisor pushes as its checkout changes.
type GitState struct {
	Changes *gitview.Changes
	// Stamps change with each edit of a changed file (its size and time).
	Stamps map[string]string
	// Err: the checkout couldn't be read (an *OpError).
	Err error
	At  time.Time
}

// ParseGitState reads a "git" message from the stream.
func ParseGitState(data json.RawMessage) (GitState, error) {
	var raw struct {
		Changes *gitview.Changes  `json:"changes"`
		Stamps  map[string]string `json:"stamps"`
		Error   string            `json:"error"`
		Code    string            `json:"code"`
		At      time.Time         `json:"at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return GitState{}, err
	}
	g := GitState{Changes: raw.Changes, Stamps: raw.Stamps, At: raw.At}
	if raw.Error != "" {
		g.Err = &OpError{Code: raw.Code, Message: raw.Error}
	}
	return g, nil
}

// FollowGit streams a session's checkout state to fn as it changes,
// starting with the current one, until ctx ends or the connection drops.
func (c *Client) FollowGit(ctx context.Context, s Session, fn func(GitState)) error {
	return c.Follow(ctx, s, []string{"git"}, func(ev string, data json.RawMessage) {
		if ev != "git" {
			return
		}
		if g, err := ParseGitState(data); err == nil {
			fn(g)
		}
	})
}

// maxKeptFile bounds a file's content kept for a parked session.
const maxKeptFile = 512 << 10

// kept is the checkout state a session's snapshot was last kept for.
type kept struct {
	Changes *gitview.Changes
	Stamps  map[string]string
}

// KeepSnapshot keeps on this machine what a parked session shows: its
// changes, every changed file's diff and contents, and its file list.
// Only what changed since the last one kept is read again.
func (c *Client) KeepSnapshot(ctx context.Context, s Session, next GitState) error {
	if next.Changes == nil {
		return next.Err
	}
	var prev *kept
	var k kept
	if _, ok := readCachedJSON(s.ID, "kept.json", &k); ok {
		prev = &k
	}
	cacheJSON(s.ID, "changes.json", next.Changes)
	old := map[string]gitview.File{}
	if prev != nil && prev.Changes != nil {
		for _, f := range prev.Changes.Files {
			old[f.Path] = f
		}
	}
	var stale []gitview.File
	var reread []string
	keep := map[string]bool{}
	listChanged := prev == nil || prev.Changes == nil
	for _, f := range next.Changes.Files {
		keep[pathKey(f.Path)] = true
		was, had := old[f.Path]
		if had && was == f && prev.Stamps[f.Path] == next.Stamps[f.Path] && cachedExists(s.ID, diffName(f.Path)) {
			continue
		}
		if !had || was.Status != f.Status {
			listChanged = true
		}
		stale = append(stale, f)
		if f.Status != gitview.Deleted && !f.Binary {
			reread = append(reread, f.Path)
		}
	}
	for p := range old {
		if !keep[pathKey(p)] {
			listChanged = true
		}
	}
	if len(stale) > 0 {
		if _, err := c.Diffs(ctx, s, stale); err != nil {
			return err
		}
	}
	if len(reread) > 0 {
		var r struct {
			Files map[string]fileContentWire `json:"files"`
		}
		if err := c.query(ctx, s, "repo.files", map[string]any{"paths": reread, "at": "work"}, &r); err != nil {
			return err
		}
		for p, fc := range r.Files {
			if fc.Size <= maxKeptFile && !fc.TooLarge {
				cacheJSON(s.ID, filepath.Join("files", pathKey(p)+".json"), fc)
			}
		}
	}
	pruneCached(s.ID, "diffs", keep)
	pruneCached(s.ID, "files", keep)
	if listChanged || !cachedExists(s.ID, "files.json") {
		if _, _, err := c.ListFiles(ctx, s); err != nil {
			return err
		}
	}
	cacheJSON(s.ID, "kept.json", kept{Changes: next.Changes, Stamps: next.Stamps})
	return nil
}

// maxPatch bounds a diff (and a file read): the rest is the editor's.
const maxPatch = 2 << 20

func diffArgs(f gitview.File, context int) map[string]any {
	return map[string]any{"path": f.Path, "oldPath": f.OldPath, "status": string(f.Status), "context": context}
}

func diffName(path string) string { return filepath.Join("diffs", pathKey(path)+".patch") }

// Diff is one file's patch against the branch's base; untracked files
// diff against nothing. context is the lines around each change (0 for
// git's default; a large number shows the whole file).
func (c *Client) Diff(ctx context.Context, s Session, f gitview.File, context int) (patch string, truncated bool, err error) {
	var r struct {
		Patch     string `json:"patch"`
		Truncated bool   `json:"truncated"`
	}
	if err := c.query(ctx, s, "repo.diff", diffArgs(f, context), &r); err != nil {
		return "", false, err
	}
	if context == 0 {
		cacheFile(s.ID, diffName(f.Path), []byte(r.Patch))
	}
	return r.Patch, r.Truncated, nil
}

// Diffs reads several files' patches at once (those too large to fit go
// unread) and keeps them for when the session is parked.
func (c *Client) Diffs(ctx context.Context, s Session, files []gitview.File) (map[string]string, error) {
	args := make([]map[string]any, len(files))
	for i, f := range files {
		args[i] = diffArgs(f, 0)
	}
	var r struct {
		Patches map[string]string `json:"patches"`
	}
	if err := c.query(ctx, s, "repo.diffs", map[string]any{"files": args}, &r); err != nil {
		return nil, err
	}
	for p, patch := range r.Patches {
		cacheFile(s.ID, diffName(p), []byte(patch))
	}
	return r.Patches, nil
}

// CachedDiff is the last diff of a file this machine read, and when.
func CachedDiff(sessionID, path string) (string, time.Time, bool) {
	p := filepath.Join(MirrorDir(sessionID), diffName(path))
	b, err := os.ReadFile(p)
	if err != nil {
		return "", time.Time{}, false
	}
	fi, _ := os.Stat(p)
	return string(b), fi.ModTime(), true
}

// FileContent is a file of the session's checkout.
type FileContent struct {
	Path string
	Data []byte
	Size int64
	// Sha is the content's sha256, for WriteFile's conflict check; "" for
	// a file that doesn't exist or is too large to edit.
	Sha      string
	Missing  bool
	TooLarge bool
	Binary   bool
}

// fileContentWire is a file as the supervisor sends it.
type fileContentWire struct {
	Data     string `json:"data"`
	Size     int64  `json:"size"`
	Sha      string `json:"sha"`
	Missing  bool   `json:"missing"`
	TooLarge bool   `json:"tooLarge"`
	Binary   bool   `json:"binary"`
}

func (w fileContentWire) content(path string) (FileContent, error) {
	data, err := base64.StdEncoding.DecodeString(w.Data)
	if err != nil {
		return FileContent{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return FileContent{Path: path, Data: data, Size: w.Size, Sha: w.Sha, Missing: w.Missing, TooLarge: w.TooLarge, Binary: w.Binary}, nil
}

// ReadFile reads a file of the session's checkout (path relative to the
// repo root).
func (c *Client) ReadFile(ctx context.Context, s Session, path string) (FileContent, error) {
	return c.FileAt(ctx, s, path, "work")
}

// FileAt reads a file as the branch's base has it ("base") or as the
// working tree does ("work").
func (c *Client) FileAt(ctx context.Context, s Session, path, at string) (FileContent, error) {
	if err := checkRelPath(path); err != nil {
		return FileContent{}, err
	}
	var w fileContentWire
	if err := c.query(ctx, s, "repo.file", map[string]string{"path": path, "at": at}, &w); err != nil {
		return FileContent{}, err
	}
	return w.content(path)
}

// CachedFile is a changed file's content as this machine last read it,
// and when.
func CachedFile(sessionID, path string) (FileContent, time.Time, bool) {
	var w fileContentWire
	at, ok := readCachedJSON(sessionID, filepath.Join("files", pathKey(path)+".json"), &w)
	if !ok {
		return FileContent{}, time.Time{}, false
	}
	fc, err := w.content(path)
	return fc, at, err == nil
}

// WriteFile saves a file of the session's checkout — unless it changed
// there since it was read: baseSha is the sha ReadFile gave ("" for a new
// file), and a mismatch is ErrConflict. id makes the save happen once.
// It returns the new sha.
func (c *Client) WriteFile(ctx context.Context, s Session, id, path string, data []byte, baseSha string) (string, error) {
	if err := checkRelPath(path); err != nil {
		return "", err
	}
	out, err := c.OpID(ctx, s, id, "write", map[string]any{
		"path": path, "data": base64.StdEncoding.EncodeToString(data), "base_sha": baseSha,
	})
	if err != nil {
		return "", err
	}
	var r struct{ Sha string }
	_ = json.Unmarshal(out, &r)
	return r.Sha, nil
}

// maxListed bounds a session's file list.
const maxListed = 50000

// ListFiles lists the session checkout's files (tracked and untracked,
// ignored ones left out), relative to the repo root.
func (c *Client) ListFiles(ctx context.Context, s Session) (paths []string, truncated bool, err error) {
	var r struct {
		Paths     []string `json:"paths"`
		Truncated bool     `json:"truncated"`
	}
	if err := c.query(ctx, s, "repo.list", nil, &r); err != nil {
		return nil, false, err
	}
	cacheJSON(s.ID, "files.json", r)
	return r.Paths, r.Truncated, nil
}

// CachedFiles is the file list this machine last read, and when.
func CachedFiles(sessionID string) (paths []string, truncated bool, at time.Time, ok bool) {
	var r struct {
		Paths     []string `json:"paths"`
		Truncated bool     `json:"truncated"`
	}
	at, ok = readCachedJSON(sessionID, "files.json", &r)
	return r.Paths, r.Truncated, at, ok
}

// Match is one search hit.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// SearchQuery is what to look for: literal text, or a regular expression
// (ripgrep's syntax).
type SearchQuery struct {
	Query         string `json:"query"`
	Regex         bool   `json:"regex,omitempty"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
}

// Search finds text in the session's checkout (tracked and untracked
// files, ignored ones left out), sending matches to fn as they are found
// until the search is done (fn's done) or ctx ends.
func (c *Client) Search(ctx context.Context, s Session, q SearchQuery, fn func(ms []Match, done, truncated bool)) error {
	if err := CheckReady(s); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failed error
	req := map[string]any{"op": "search", "id": newJobID(), "args": q}
	err := c.followUpgrading(ctx, s, nil, []any{req}, func(m streamMsg) {
		if m.Ev != "search" {
			return
		}
		var d struct {
			Matches   []Match `json:"matches"`
			Done      bool    `json:"done"`
			Truncated bool    `json:"truncated"`
		}
		json.Unmarshal(m.Data, &d)
		if m.Err != "" {
			failed = &OpError{Code: m.Code, Message: m.Err}
			cancel()
			return
		}
		fn(d.Matches, d.Done, d.Truncated)
		if d.Done {
			cancel()
		}
	})
	if failed != nil {
		return failed
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// Commit commits everything in the session's checkout (git add -A) and
// returns the new HEAD. id makes it happen once.
func (c *Client) Commit(ctx context.Context, s Session, id, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("a commit needs a message")
	}
	out, err := c.OpID(ctx, s, id, "git", map[string]string{"action": "commit", "message": message})
	if err != nil {
		return "", err
	}
	var r struct{ Head string }
	_ = json.Unmarshal(out, &r)
	return r.Head, nil
}

// Push pushes the session's branch to origin, setting its upstream. id
// makes it happen once.
func (c *Client) Push(ctx context.Context, s Session, id string) error {
	_, err := c.OpID(ctx, s, id, "git", map[string]string{"action": "push"})
	return err
}

// checkRelPath refuses paths that leave the checkout.
func checkRelPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(filepath.Clean(p), "..") {
		return fmt.Errorf("%q isn't a path in the checkout", p)
	}
	return nil
}

func pathKey(p string) string {
	h := sha1.Sum([]byte(p))
	return hex.EncodeToString(h[:8])
}

func cacheFile(sessionID, name string, b []byte) {
	p := filepath.Join(MirrorDir(sessionID), name)
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
		_ = writeFileAtomic(p, b)
	}
}

func cacheJSON(sessionID, name string, v any) {
	if b, err := json.Marshal(v); err == nil {
		cacheFile(sessionID, name, b)
	}
}

func cachedExists(sessionID, name string) bool {
	_, err := os.Stat(filepath.Join(MirrorDir(sessionID), name))
	return err == nil
}

// pruneCached removes kept files of a directory whose key isn't in keep.
func pruneCached(sessionID, dir string, keep map[string]bool) {
	ents, _ := os.ReadDir(filepath.Join(MirrorDir(sessionID), dir))
	for _, e := range ents {
		key, _, _ := strings.Cut(e.Name(), ".")
		if !keep[key] {
			os.Remove(filepath.Join(MirrorDir(sessionID), dir, e.Name()))
		}
	}
}

func readCachedJSON(sessionID, name string, v any) (time.Time, bool) {
	p := filepath.Join(MirrorDir(sessionID), name)
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, v) != nil {
		return time.Time{}, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

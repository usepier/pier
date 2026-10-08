package pier

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier/gitview"
)

// Reviewing a session's work from the laptop: what changed against the
// branch's base, each file's diff, the files themselves, and search. Reads
// run as one-off commands over the session's connection; changes (a saved
// file, a commit, a push) run inside its supervisor, so a dropped
// connection can't leave one half done and a retried id is a no-op.

// ErrConflict: the file changed in the session since it was read.
var ErrConflict = errors.New("the file changed in the session since it was read")

// repoDir is the session's checkout, in a script.
func repoDir(s Session) string { return "~/work/" + shellQuote(s.Repo) }

// mergeBase sets $mb to the commit the branch started from: its merge
// base with the origin branch it came off, else the commit pier recorded
// at create. $ref is the origin branch ("" when unknown).
const mergeBase = `ref=""; b=$(git config pier.base 2>/dev/null)
if [ -n "$b" ] && git rev-parse -q --verify "refs/remotes/origin/$b" >/dev/null; then ref="origin/$b"; fi
mb=""; [ -n "$ref" ] && mb=$(git merge-base HEAD "$ref" 2>/dev/null)
[ -z "$mb" ] && mb=$(git rev-parse -q --verify refs/pier/base 2>/dev/null)
[ -z "$mb" ] && mb=$(git rev-parse -q --verify HEAD 2>/dev/null)
`

const section = "\n--pier-section--\n"

// Changes reads how the session's checkout differs from its base. A copy
// is kept on this machine for when the session is parked (CachedChanges).
func (c *Client) Changes(ctx context.Context, s Session) (gitview.Changes, error) {
	if err := CheckReady(s); err != nil {
		return gitview.Changes{}, err
	}
	script := "cd " + repoDir(s) + " || exit 3\n" + mergeBase + `printf '%s\n%s' "$ref" "$mb"
printf '` + section + `'; git status --porcelain=v2 -z --branch --untracked-files=all
printf '` + section + `'; [ -n "$mb" ] && git diff --name-status -z -M "$mb"
printf '` + section + `'; [ -n "$mb" ] && git diff --numstat -z -M "$mb"
printf '` + section + `'; git ls-files -o --exclude-standard -z | xargs -0 -n 50 wc -l 2>/dev/null | head -n 400
true`
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return gitview.Changes{}, err
	}
	parts := strings.Split(out, section)
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	ref, mb, _ := strings.Cut(parts[0], "\n")
	ch := gitview.Parse(gitview.Snapshot{
		BaseRef: strings.TrimSpace(ref), BaseOid: strings.TrimSpace(mb),
		Status: parts[1], NameStatus: parts[2], Numstat: parts[3], UntrackedLines: parts[4],
	})
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

// maxPatch bounds a diff (and a file read): the rest is the editor's.
const maxPatch = 2 << 20

// Diff is one file's patch against the branch's base; untracked files
// diff against nothing. context is the lines around each change (0 for
// git's default; a large number shows the whole file).
func (c *Client) Diff(ctx context.Context, s Session, f gitview.File, context int) (patch string, truncated bool, err error) {
	if err := CheckReady(s); err != nil {
		return "", false, err
	}
	u := ""
	if context > 0 {
		u = " -U" + strconv.Itoa(context)
	}
	var diff string
	if f.Status == gitview.Untracked {
		diff = "git diff --no-index" + u + " -- /dev/null " + shellQuote(f.Path)
	} else {
		paths := shellQuote(f.Path)
		if f.OldPath != "" {
			paths = shellQuote(f.OldPath) + " " + paths
		}
		diff = `git diff -M` + u + ` "$mb" -- ` + paths
	}
	script := "cd " + repoDir(s) + " || exit 3\n" + mergeBase + diff + " | head -c " + strconv.Itoa(maxPatch+1) + "\ntrue"
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return "", false, err
	}
	if len(out) > maxPatch {
		out, truncated = out[:maxPatch], true
	}
	cacheFile(s.ID, filepath.Join("diffs", pathKey(f.Path)+".patch"), []byte(out))
	return out, truncated, nil
}

// CachedDiff is the last diff of a file this machine read, and when.
func CachedDiff(sessionID, path string) (string, time.Time, bool) {
	p := filepath.Join(MirrorDir(sessionID), "diffs", pathKey(path)+".patch")
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
}

// ReadFile reads a file of the session's checkout (path relative to the
// repo root).
func (c *Client) ReadFile(ctx context.Context, s Session, path string) (FileContent, error) {
	if err := CheckReady(s); err != nil {
		return FileContent{}, err
	}
	if err := checkRelPath(path); err != nil {
		return FileContent{}, err
	}
	script := "cd " + repoDir(s) + " || exit 3\nf=" + shellQuote(path) + `
[ -f "$f" ] || { echo missing; exit 0; }
wc -c < "$f"
head -c ` + strconv.Itoa(maxPatch+1) + ` "$f" | base64 | tr -d '\n'`
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return FileContent{}, err
	}
	head, body, _ := strings.Cut(out, "\n")
	if strings.TrimSpace(head) == "missing" {
		return FileContent{Path: path, Missing: true}, nil
	}
	fc := FileContent{Path: path}
	fc.Size, _ = strconv.ParseInt(strings.TrimSpace(head), 10, 64)
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
	if err != nil {
		return FileContent{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) > maxPatch {
		data, fc.TooLarge = data[:maxPatch], true
	} else {
		// The supervisor checks a save against the same sum.
		sum := sha256.Sum256(data)
		fc.Sha = hex.EncodeToString(sum[:])
	}
	fc.Data = data
	return fc, nil
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
		"repo": s.Repo, "path": path, "data": base64.StdEncoding.EncodeToString(data), "base_sha": baseSha,
	})
	if err != nil {
		if strings.Contains(err.Error(), "conflict:") {
			return "", ErrConflict
		}
		return "", err
	}
	var r struct{ Sha string }
	_ = json.Unmarshal(out, &r)
	return r.Sha, nil
}

// Match is one search hit.
type Match struct {
	Path string
	Line int
	Col  int
	Text string
}

// maxMatches bounds a search.
const maxMatches = 2000

// Search finds text in the session's checkout (tracked and untracked
// files, ignored ones left out). regex reads the query as an extended
// regular expression; otherwise it is literal.
func (c *Client) Search(ctx context.Context, s Session, query string, regex, caseSensitive bool) (matches []Match, truncated bool, err error) {
	if err := CheckReady(s); err != nil {
		return nil, false, err
	}
	if query == "" {
		return nil, false, nil
	}
	flags := " -F"
	if regex {
		flags = " -E"
	}
	if !caseSensitive {
		flags += " -i"
	}
	script := "cd " + repoDir(s) + " || exit 3\ngit grep --untracked -n --column -I -z" + flags + " -e " + shellQuote(query) +
		" | head -n " + strconv.Itoa(maxMatches+1) + "\ntrue"
	out, err := c.exec(ctx, s.ID, script)
	if err != nil {
		return nil, false, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		parts := strings.SplitN(line, "\x00", 4)
		if len(parts) != 4 {
			continue
		}
		if len(matches) == maxMatches {
			return matches, true, nil
		}
		m := Match{Path: parts[0], Text: parts[3]}
		m.Line, _ = strconv.Atoi(parts[1])
		m.Col, _ = strconv.Atoi(parts[2])
		if len(m.Text) > 400 {
			m.Text = m.Text[:400]
		}
		matches = append(matches, m)
	}
	return matches, false, nil
}

// Commit commits everything in the session's checkout (git add -A) and
// returns the new HEAD. id makes it happen once.
func (c *Client) Commit(ctx context.Context, s Session, id, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("a commit needs a message")
	}
	out, err := c.OpID(ctx, s, id, "git", map[string]string{"repo": s.Repo, "action": "commit", "message": message})
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
	_, err := c.OpID(ctx, s, id, "git", map[string]string{"repo": s.Repo, "action": "push"})
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

package pier

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The agent's conversation is read from the transcript its CLI writes in
// the session. The laptop keeps a copy of it (a mirror), brought up to date
// from the session's stream, so the chat shows while the session is parked
// and a reconnect only fetches what is new.

// TranscriptChunk is one piece of the agent's transcript from the stream
// (a "tx" message): complete lines from Off to Next of File.
type TranscriptChunk struct {
	File string `json:"file"` // "" while the agent has no transcript yet
	Kind string `json:"kind"` // claude | codex
	Off  int64  `json:"off"`
	Next int64  `json:"next"`
	Data string `json:"data"`
	// Reset: Data starts a different file (a new conversation) or the file
	// was rewritten — drop the copy first.
	Reset bool `json:"reset"`
	// EOF: caught up, for now.
	EOF     bool `json:"eof"`
	Skipped int  `json:"skipped"` // lines too long to send
}

// ErrMirrorGap: a chunk doesn't continue the copy (it was cut short, or is
// of another file): follow again from the start.
var ErrMirrorGap = errors.New("the transcript copy is out of step")

// Mirror is this machine's copy of one session's agent transcript. One
// process keeps it up to date at a time; another opening it meanwhile
// works on a private copy.
type Mirror struct {
	dir   string
	meta  MirrorMeta
	close func()
}

// MirrorMeta says what the copy is of.
type MirrorMeta struct {
	File string `json:"file"` // the transcript's path in the session
	Kind string `json:"kind"`
	// Next is how far into the transcript the copy reaches; Len is the
	// copy's own length (shorter by any lines left out as too long).
	Next   int64     `json:"next"`
	Len    int64     `json:"len"`
	Synced time.Time `json:"synced"`
	// Skipped counts lines left out.
	Skipped int `json:"skipped,omitempty"`
}

// MirrorDir is where a session's copies live.
func MirrorDir(sessionID string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "pier", "sessions", filepath.Base(sessionID))
}

// OpenMirror opens (or starts) a session's transcript copy. Close it when
// done.
func OpenMirror(sessionID string) (*Mirror, error) {
	m := &Mirror{dir: filepath.Join(MirrorDir(sessionID), "chat")}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(m.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		m.close = func() { lock.Close() }
	} else {
		lock.Close()
		// Another pier keeps this copy: start from it, privately.
		private, err := os.MkdirTemp("", "pier-chat-")
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"meta.json", "transcript.jsonl"} {
			if b, err := os.ReadFile(filepath.Join(m.dir, name)); err == nil {
				os.WriteFile(filepath.Join(private, name), b, 0o600)
			}
		}
		m.dir, m.close = private, func() { os.RemoveAll(private) }
	}
	if b, err := os.ReadFile(m.metaPath()); err == nil {
		_ = json.Unmarshal(b, &m.meta)
	}
	// A crash between appending and recording leaves extra bytes: the
	// record is the truth.
	fi, err := os.Stat(m.dataPath())
	switch {
	case err != nil || fi.Size() < m.meta.Len:
		m.meta = MirrorMeta{}
		_ = os.WriteFile(m.dataPath(), nil, 0o600)
	case fi.Size() > m.meta.Len:
		if err := os.Truncate(m.dataPath(), m.meta.Len); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Mirror) metaPath() string { return filepath.Join(m.dir, "meta.json") }
func (m *Mirror) dataPath() string { return filepath.Join(m.dir, "transcript.jsonl") }

// Close lets another process keep the copy.
func (m *Mirror) Close() {
	if m.close != nil {
		m.close()
		m.close = nil
	}
}

// Meta says what the copy holds.
func (m *Mirror) Meta() MirrorMeta { return m.meta }

// Lines returns the copy's lines, oldest first.
func (m *Mirror) Lines() ([][]byte, error) {
	f, err := os.Open(m.dataPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]byte
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			out = append(out, bytes.TrimRight(line, "\n"))
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// Apply adds a chunk to the copy. A chunk that doesn't continue it (and
// doesn't reset it) is ErrMirrorGap.
func (m *Mirror) Apply(ch TranscriptChunk) error {
	if ch.Reset || ch.File != m.meta.File {
		if ch.Off != 0 {
			return ErrMirrorGap
		}
		if err := os.WriteFile(m.dataPath(), nil, 0o600); err != nil {
			return err
		}
		m.meta = MirrorMeta{File: ch.File, Kind: ch.Kind}
	} else if ch.Off != m.meta.Next {
		return ErrMirrorGap
	}
	if ch.Data != "" {
		f, err := os.OpenFile(m.dataPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(ch.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	m.meta.Len += int64(len(ch.Data))
	m.meta.Next = ch.Next
	m.meta.Kind = ch.Kind
	m.meta.Skipped += ch.Skipped
	m.meta.Synced = time.Now().UTC()
	b, _ := json.Marshal(m.meta)
	return writeFileAtomic(m.metaPath(), b)
}

// Reset empties the copy.
func (m *Mirror) Reset() error {
	m.meta = MirrorMeta{}
	_ = os.Remove(m.dataPath())
	return os.Remove(m.metaPath())
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PruneMirrors removes the copies of sessions that are gone: not in ids
// and untouched for a week (a session listed by another cloud config
// keeps its copy a while).
func PruneMirrors(ids []string) {
	root := filepath.Dir(MirrorDir("x"))
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if keep[e.Name()] || !e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 7*24*time.Hour {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

var uploadExt = regexp.MustCompile(`^\.[A-Za-z0-9]{1,8}$`)

// maxUpload bounds a file put into a session through the chat.
const maxUpload = 20 << 20

// Upload puts a file (an image pasted into the chat) into the session and
// returns its path there, for the prompt to name.
func (c *Client) Upload(ctx context.Context, s Session, name string, data []byte) (string, error) {
	if len(data) > maxUpload {
		return "", fmt.Errorf("%s is over %d MB", name, maxUpload>>20)
	}
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !uploadExt.MatchString(ext) {
		ext = ""
	}
	b := make([]byte, 8)
	rand.Read(b)
	file := hex.EncodeToString(b) + ext
	out, err := conn.Exec(ctx, `mkdir -p ~/.pier/uploads && cat > ~/.pier/uploads/`+file+` && echo "$HOME/.pier/uploads/`+file+`"`, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// maxListed bounds a session's file list.
const maxListed = 50000

// ListFiles lists the session checkout's files (tracked and untracked,
// ignored ones left out), relative to the repo root, for @-mentions.
func (c *Client) ListFiles(ctx context.Context, s Session) (paths []string, truncated bool, err error) {
	if err := CheckReady(s); err != nil {
		return nil, false, err
	}
	out, err := c.exec(ctx, s.ID, "cd ~/work/"+shellQuote(s.Repo)+" && git ls-files -z --cached --others --exclude-standard | head -c 8000000")
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] {
			continue
		}
		if len(paths) == maxListed {
			return paths, true, nil
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths, len(out) >= 8000000, nil
}

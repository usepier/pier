package pier

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

// A chat's items live in the session (its log, ~/.pier/chats/<id>); the
// laptop keeps a copy (a mirror), brought up to date from the session's
// stream, so the chat shows while the session is parked and a reconnect
// only fetches what is new. The copy is capped: past a size it keeps the
// newest items, each once, and older ones page in from the session.

// LogChunk is one piece of a chat's log from the stream (a "log" message):
// complete lines from Off to Next of File.
type LogChunk struct {
	Chat string `json:"chat"`
	File string `json:"file"` // the log's name in the session
	Off  int64  `json:"off"`
	Next int64  `json:"next"`
	Data string `json:"data"`
	// Reset: Data starts another log (the session compacted it): drop the
	// copy first.
	Reset bool `json:"reset"`
	// EOF: caught up, for now.
	EOF     bool `json:"eof"`
	Skipped int  `json:"skipped"` // lines too long to send
}

// ErrMirrorGap: a chunk doesn't continue the copy (it was cut short, or is
// of another log): follow again from the start.
var ErrMirrorGap = errors.New("the chat copy is out of step")

// Mirror is this machine's copy of one chat's log. One process keeps it
// up to date at a time; another opening it meanwhile works on a private
// copy.
type Mirror struct {
	dir   string
	meta  MirrorMeta
	close func()
}

// MirrorMeta says what the copy is of.
type MirrorMeta struct {
	File string `json:"file"` // the log's name in the session
	// Next is how far into the log the copy reaches; Len is the copy's own
	// length (it is compacted, so shorter).
	Next   int64     `json:"next"`
	Len    int64     `json:"len"`
	Synced time.Time `json:"synced"`
	// Skipped counts lines left out; Dropped says the oldest items were
	// let go (the session still has them).
	Skipped int  `json:"skipped,omitempty"`
	Dropped bool `json:"dropped,omitempty"`
}

// Limits of a chat's copy: past maxMirror bytes it is compacted to its
// items, the newest keepItems of them.
const (
	maxMirror = 16 << 20
	keepItems = 3000
)

// MirrorDir is where a session's copies live.
func MirrorDir(sessionID string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "pier", "sessions", filepath.Base(sessionID))
}

// ChatMirrorDir is one chat's copy.
func ChatMirrorDir(sessionID, chatID string) string {
	return filepath.Join(MirrorDir(sessionID), "chats", filepath.Base(chatID))
}

// ChatMirrors lists the chats of a session this machine has copies of.
func ChatMirrors(sessionID string) []string {
	ents, _ := os.ReadDir(filepath.Join(MirrorDir(sessionID), "chats"))
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// OpenMirror opens (or starts) a chat's copy. Close it when done.
func OpenMirror(sessionID, chatID string) (*Mirror, error) {
	m := &Mirror{dir: ChatMirrorDir(sessionID, chatID)}
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
		for _, name := range []string{"meta.json", "items.jsonl"} {
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
func (m *Mirror) dataPath() string { return filepath.Join(m.dir, "items.jsonl") }

// Close lets another process keep the copy.
func (m *Mirror) Close() {
	if m.close != nil {
		m.close()
		m.close = nil
	}
}

// Meta says what the copy holds.
func (m *Mirror) Meta() MirrorMeta { return m.meta }

// Items reads the copy: each item once, as last written, in the order
// they first appeared.
func (m *Mirror) Items() ([]wire.ChatItem, error) {
	f, err := os.Open(m.dataPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return foldItems(f)
}

// foldItems reads item lines, keeping the last of each id.
func foldItems(r io.Reader) ([]wire.ChatItem, error) {
	var items []wire.ChatItem
	index := map[string]int{}
	br := bufio.NewReaderSize(r, 256<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var it wire.ChatItem
			if json.Unmarshal(line, &it) == nil && it.ID != "" {
				if i, ok := index[it.ID]; ok {
					items[i] = it
				} else {
					index[it.ID] = len(items)
					items = append(items, it)
				}
			}
		}
		if err == io.EOF {
			return items, nil
		}
		if err != nil {
			return items, err
		}
	}
}

// ParseItems reads a chunk's lines into items, in order (an id can
// repeat: the later line is the item's newer state).
func ParseItems(data string) []wire.ChatItem {
	var out []wire.ChatItem
	for _, line := range bytes.Split([]byte(data), []byte{'\n'}) {
		var it wire.ChatItem
		if len(line) > 0 && json.Unmarshal(line, &it) == nil && it.ID != "" {
			out = append(out, it)
		}
	}
	return out
}

// Apply adds a chunk to the copy. A chunk that doesn't continue it (and
// doesn't reset it) is ErrMirrorGap.
func (m *Mirror) Apply(ch LogChunk) error {
	if ch.Reset || ch.File != m.meta.File {
		if ch.Off != 0 {
			return ErrMirrorGap
		}
		if err := os.WriteFile(m.dataPath(), nil, 0o600); err != nil {
			return err
		}
		m.meta = MirrorMeta{File: ch.File}
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
	m.meta.Skipped += ch.Skipped
	m.meta.Synced = time.Now().UTC()
	if m.meta.Len > maxMirror {
		if err := m.compact(); err != nil {
			return err
		}
	}
	return m.saveMeta()
}

func (m *Mirror) saveMeta() error {
	b, _ := json.Marshal(m.meta)
	return writeFileAtomic(m.metaPath(), b)
}

// compact rewrites the copy as its newest keepItems items, once each.
func (m *Mirror) compact() error {
	items, err := m.Items()
	if err != nil {
		return err
	}
	if len(items) > keepItems {
		items = items[len(items)-keepItems:]
		m.meta.Dropped = true
	}
	var b bytes.Buffer
	for _, it := range items {
		line, _ := json.Marshal(it)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := writeFileAtomic(m.dataPath(), b.Bytes()); err != nil {
		return err
	}
	m.meta.Len = int64(b.Len())
	return nil
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

// PruneChatMirrors removes the copies of a session's chats that are no
// longer among ids (closed in the session).
func PruneChatMirrors(sessionID string, ids []string) {
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	for _, id := range ChatMirrors(sessionID) {
		if !keep[id] {
			os.RemoveAll(ChatMirrorDir(sessionID, id))
		}
	}
}

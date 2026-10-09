package main

// A chat's log: ~/.pier/chats/<id>/log-<gen>.jsonl, one chat item per
// line, appended as the agent makes and finishes them. An item can appear
// more than once (a tool call, then the call with its result); readers keep
// the last of each id. Frontends copy the log by offset (tail), so a
// reconnect fetches only what is new; compacting starts a new generation,
// which a frontend's copy notices by its name.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/wire"
)

const (
	tailChunk   = 256 << 10 // bytes per message, at most (bar one long line)
	tailMaxLine = 4 << 20   // a longer line is skipped, not sent
	// compactAt: a log this much larger than its items is rewritten.
	compactAt = 4 << 20
)

// chatLog appends to one chat's log and wakes its tails.
type chatLog struct {
	mu   sync.Mutex
	dir  string
	name string // log-<gen>.jsonl
	f    *os.File
	// wake closes when the log grows (or turns over): tails wait on it.
	wake chan struct{}
}

func openChatLog(dir, name string) (*chatLog, error) {
	if name == "" {
		name = "log-1.jsonl"
	}
	l := &chatLog{dir: dir, name: name, wake: make(chan struct{})}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *chatLog) open() error {
	f, err := os.OpenFile(filepath.Join(l.dir, l.name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

func (l *chatLog) path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return filepath.Join(l.dir, l.name)
}

// waitCh is closed at the log's next change.
func (l *chatLog) waitCh() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.wake
}

func (l *chatLog) changed() {
	close(l.wake)
	l.wake = make(chan struct{})
}

// put appends items.
func (l *chatLog) put(items ...wire.ChatItem) error {
	if len(items) == 0 {
		return nil
	}
	var b bytes.Buffer
	for _, it := range items {
		line, err := json.Marshal(it)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.f.Write(b.Bytes())
	l.changed()
	return err
}

func (l *chatLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// readItems folds a log into its items, in order of first appearance.
func readItems(path string) ([]wire.ChatItem, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var items []wire.ChatItem
	index := map[string]int{}
	r := bufio.NewReaderSize(f, 256<<10)
	var size int64
	for {
		line, err := r.ReadBytes('\n')
		size += int64(len(line))
		if len(line) > 0 && line[len(line)-1] == '\n' {
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
			return items, size, nil
		}
		if err != nil {
			return items, size, err
		}
	}
}

// compact rewrites the log as its items, once each, when repeats make it
// much larger than they are: a new generation, the old one removed.
func (l *chatLog) compact() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	old := filepath.Join(l.dir, l.name)
	items, size, err := readItems(old)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	for _, it := range items {
		line, _ := json.Marshal(it)
		b.Write(line)
		b.WriteByte('\n')
	}
	if size-int64(b.Len()) < compactAt {
		return nil
	}
	next := fmt.Sprintf("log-%d.jsonl", logGen(l.name)+1)
	if err := writeAtomic(filepath.Join(l.dir, next), b.Bytes()); err != nil {
		return err
	}
	l.f.Close()
	l.name = next
	if err := l.open(); err != nil {
		return err
	}
	os.Remove(old)
	l.changed()
	return nil
}

func logGen(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "log-"), ".jsonl"))
	return n
}

// logChunk is one "log" message: complete lines of a chat's log.
type logChunk struct {
	Chat string `json:"chat"`
	File string `json:"file"` // the log's name: a new one means start over
	Off  int64  `json:"off"`  // where Data starts in the file
	Next int64  `json:"next"` // where the next chunk starts
	Data string `json:"data,omitempty"`
	// Reset says the frontend's copy is of another file: start over from
	// Data.
	Reset bool `json:"reset,omitempty"`
	// EOF says this is all there is for now.
	EOF bool `json:"eof,omitempty"`
	// Skipped counts lines too long to send.
	Skipped int `json:"skipped,omitempty"`
}

// tailLog sends a chat's log to a client from (file, off), and what is
// appended to it, until stop closes.
func tailLog(c *client, chatID string, l *chatLog, file string, off int64, stop <-chan struct{}, onCaughtUp func()) {
	announced, sentEOF, caughtUp := false, false, false
	for {
		wake := l.waitCh()
		path := l.path()
		name := filepath.Base(path)
		reset := false
		if name != file {
			file, off, reset = name, 0, true
		}
		size := int64(-1)
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
		if size >= 0 && size < off {
			off, reset = 0, true
		}
		ch := logChunk{Chat: chatID, File: file, Off: off, Next: off, Reset: reset, EOF: true}
		if size > off {
			data, next, skipped, more := readLines(path, off, tailChunk)
			ch.Data, ch.Next, ch.Skipped, ch.EOF = string(data), next, skipped, !more
		}
		if reset || !announced || ch.Next > ch.Off || (ch.EOF && !sentEOF) {
			if c.send(message{Ev: "log", Data: mustJSON(ch)}) != nil {
				return
			}
			announced, sentEOF = true, ch.EOF
			off = ch.Next
			if !ch.EOF {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
		}
		if !caughtUp && onCaughtUp != nil {
			caughtUp = true
			onCaughtUp()
		}
		select {
		case <-stop:
			return
		case <-wake:
		case <-time.After(30 * time.Second): // a missed wake costs at most this
		}
	}
}

// readLines reads the complete lines of a file from off, about chunk bytes
// of them. more says complete lines may remain past next.
func readLines(file string, off int64, chunk int) (data []byte, next int64, skipped int, more bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, off, 0, false
	}
	defer f.Close()
	buf := make([]byte, chunk)
	n, _ := io.ReadFull(io.NewSectionReader(f, off, 1<<62), buf)
	buf = buf[:n]
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		return buf[:i+1], off + int64(i) + 1, 0, n == chunk
	}
	if n < chunk {
		return nil, off, 0, false // a line still being written
	}
	// One line longer than a chunk: send it whole unless it is huge, then
	// skip past it.
	nl := indexNewline(f, off+int64(n))
	if nl < 0 {
		return nil, off, 0, false
	}
	next = nl + 1
	fi, err := f.Stat()
	more = err == nil && next < fi.Size()
	if next-off > int64(tailMaxLine) {
		return nil, next, 1, more
	}
	line := make([]byte, next-off)
	if _, err := f.ReadAt(line, off); err != nil {
		return nil, off, 0, false
	}
	return line, next, 0, more
}

// indexNewline finds the first newline at or after pos; -1 when none.
func indexNewline(f *os.File, pos int64) int64 {
	part := make([]byte, 64<<10)
	for {
		m, err := f.ReadAt(part, pos)
		if i := bytes.IndexByte(part[:m], '\n'); i >= 0 {
			return pos + int64(i)
		}
		if err != nil {
			return -1
		}
		pos += int64(m)
	}
}

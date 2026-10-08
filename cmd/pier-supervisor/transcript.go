package main

// A frontend reads the agent's conversation from the transcript the agent
// CLI writes as it goes, tailed here and sent over the stream as "tx"
// messages: complete lines only, from the offset the frontend already has.
// A reconnect picks up where the last connection stopped, and the frontend
// keeps its own copy to show while the session is parked.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	tailChunk   = 256 << 10 // bytes per message, at most (bar one long line)
	tailMaxLine = 4 << 20   // a longer line is skipped, not sent
	tailEvery   = 300 * time.Millisecond
)

// txChunk is one "tx" message.
type txChunk struct {
	File string `json:"file"` // "" while the agent has no transcript yet
	Kind string `json:"kind,omitempty"`
	Off  int64  `json:"off"`  // where Data starts in the file
	Next int64  `json:"next"` // where the next chunk starts
	Data string `json:"data,omitempty"`
	// Reset says the frontend's copy is of another file (a new
	// conversation) or the file was rewritten: start over from Data.
	Reset bool `json:"reset,omitempty"`
	// EOF says this is all there is for now.
	EOF bool `json:"eof,omitempty"`
	// Skipped counts lines too long to send.
	Skipped int `json:"skipped,omitempty"`
}

// tail sends the agent's transcript to a client from (file, off) until stop
// closes, following the agent onto a new transcript when its conversation
// changes.
func (h *hub) tail(c *client, file string, off int64, stop <-chan struct{}) {
	h.mu.Lock()
	find, every, chunk := h.findTranscript, h.tailEvery, h.tailChunk
	h.mu.Unlock()
	announced, sentEOF := false, false
	for {
		path, kind := "", ""
		if find != nil {
			path, kind = find()
		}
		reset := false
		if path != file {
			file, off, reset = path, 0, true
		}
		size := int64(-1)
		if file != "" {
			if fi, err := os.Stat(file); err == nil {
				size = fi.Size()
			}
		}
		if size >= 0 && size < off {
			off, reset = 0, true // rewritten
		}
		ch := txChunk{File: file, Kind: kind, Off: off, Next: off, Reset: reset, EOF: true}
		if size > off {
			data, next, skipped, more := readLines(file, off, chunk)
			ch.Data, ch.Next, ch.Skipped, ch.EOF = string(data), next, skipped, !more
		}
		if reset || !announced || ch.Next > ch.Off || (ch.EOF && !sentEOF) {
			if c.send(message{Ev: "tx", Data: mustJSON(ch)}) != nil {
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
		select {
		case <-stop:
			return
		case <-time.After(every):
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
		return nil, off, 0, false // still being written
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

// transcript is the transcript of the agent prompts go to (or, with none
// running, the last one that ran) and its kind.
func (a *agents) transcript() (path, kind string) {
	s := a.primary()
	if s == nil {
		s = a.latest()
	}
	if s == nil {
		return "", ""
	}
	return findTranscript(a.home, *s), s.Kind
}

// latest is the newest agent, ended or not.
func (a *agents) latest() *agentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	var p *agentState
	for _, s := range a.byPane {
		if p == nil || s.Since.After(p.Since) {
			p = s
		}
	}
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// findTranscript locates an agent's transcript: where its hooks said, else
// where its CLI keeps the conversation it is on.
func findTranscript(home string, s agentState) string {
	inHome := func(p string) bool {
		p = filepath.Clean(p)
		return strings.HasPrefix(p, filepath.Clean(home)+"/") && strings.HasSuffix(p, ".jsonl")
	}
	if s.Transcript != "" && inHome(s.Transcript) {
		if _, err := os.Stat(s.Transcript); err == nil {
			return filepath.Clean(s.Transcript)
		}
	}
	var pattern string
	switch {
	case s.Kind == "codex" && s.Conversation != "":
		pattern = filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+filepath.Base(s.Conversation)+".jsonl")
	case s.Kind == "codex":
		// Not known yet: the newest conversation since it started.
		pattern = filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl")
	case s.Conversation != "":
		pattern = filepath.Join(home, ".claude", "projects", "*", filepath.Base(s.Conversation)+".jsonl")
	default:
		return ""
	}
	matches, _ := filepath.Glob(pattern)
	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if s.Conversation == "" && fi.ModTime().Before(s.Since.Add(-time.Minute)) {
			continue
		}
		cs = append(cs, cand{m, fi.ModTime()})
	}
	if len(cs) == 0 {
		return ""
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].mod.After(cs[j].mod) })
	return cs[0].path
}

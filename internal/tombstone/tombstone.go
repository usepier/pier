// Package tombstone remembers creates that died before the session existed.
//
// A failed create still terminates its instance — a half-made VM costs money
// and can't be resumed into a working session, so leaving it running helps
// nobody. What was missing was any trace afterwards: the instance vanished,
// and with it every hint that the user had asked for a session at all. Eight
// creates would return six sessions and no explanation.
//
// A tombstone is that trace: a small local record, written by the create that
// failed, that rides along in the session list as a failed row until it's
// dismissed. It holds the reason and a pointer to the create log, so "why did
// it go" is answerable after the fact. Records are local-only — they describe
// something that never reached the cloud, so there's nothing to reconcile.
package tombstone

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// retention bounds the graveyard. A tombstone is a nudge to look at a recent
// failure, not history worth keeping: after a week the log it points at is
// stale and the row is just clutter, so List drops it on the next read.
const retention = 7 * 24 * time.Hour

// Record is one failed create.
type Record struct {
	Name    string    `json:"name"`
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Driver  string    `json:"driver"`
	Reason  string    `json:"reason"`   // the create error, verbatim
	LogPath string    `json:"log_path"` // create log, when the create wrote one
	When    time.Time `json:"when"`
}

// Summary is Reason's first line, trimmed for a table cell. The full text
// stays in the record for `pier logs`-style detail.
func (r Record) Summary() string { return Summarize(r.Reason) }

// Summarize reduces a create error to one short line. Create errors carry
// whole bootstrap transcripts; a list row wants the headline.
func Summarize(reason string) string {
	s, _, _ := strings.Cut(strings.TrimSpace(reason), "\n")
	s = strings.TrimPrefix(s, "pier: ")
	const max = 72
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	// Elide the middle, not the tail. These errors open with the operation
	// and close with the cause — "scp <long temp path> -> <instance id>: ssh:
	// connect to host …: Network is unreachable" — and the cause is the half
	// worth reading. Trimming the end would keep only the temp path.
	const head = 26
	return string(r[:head]) + "…" + string(r[len(r)-(max-head-1):])
}

func dir(stateDir string) string { return filepath.Join(stateDir, "failed") }

// file is where one session's record lives.
func file(stateDir, name string) string {
	return filepath.Join(dir(stateDir), FileStem(name)+".json")
}

// FileStem turns a session name into a file name that is safe, unique and
// still readable. pier keys two per-session files by name — the create log
// and the failed-create record — and both need that guarantee.
//
// Escaped, not folded. Names may hold both "/" and "-" (see
// payload.checkName), so mapping one onto the other would give feat/login and
// feat-login a single file to fight over: one create's output or failure
// silently replacing the other's, and dismissing one clearing the other.
// Percent escaping is reversible, so distinct names stay distinct — while a
// name needing no escaping, which is nearly all of them, is spelled exactly
// as the user typed it. It also leaves no "/" to walk out of the directory.
func FileStem(name string) string {
	var b strings.Builder
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c) // raw bytes, so no two names escape alike
		}
	}
	s := b.String()
	if s == "" {
		return "unnamed"
	}
	// Names arrive unvalidated — a create can fail on the name itself, and
	// that failure still deserves a record — so cap the length rather than
	// hand the filesystem something it will reject. The digest keeps
	// truncated forms apart.
	if len(s) > 120 {
		sum := sha256.Sum256([]byte(name))
		s = s[:112] + hex.EncodeToString(sum[:4])
	}
	return s
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// freePath is where a displaced record can go without destroying anything:
// its canonical path, or the first numbered variant that is free. Records are
// located by the name stored inside them rather than by their file name, so a
// numbered variant is fully functional — it only has to not collide.
func freePath(stateDir, name string) string {
	p := file(stateDir, name)
	for i := 1; exists(p) && i < 1000; i++ {
		p = filepath.Join(dir(stateDir), fmt.Sprintf("%s.%d.json", FileStem(name), i))
	}
	return p
}

// entry is one file in the graveyard alongside the record it holds.
type entry struct {
	path string
	rec  Record
}

// entries reads the graveyard. Unreadable or malformed files are skipped
// rather than fatal: a corrupt tombstone must never break `pier ls`.
func entries(stateDir string) []entry {
	ents, err := os.ReadDir(dir(stateDir))
	if err != nil {
		return nil
	}
	var out []entry
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir(stateDir), e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil || r.Name == "" {
			continue
		}
		out = append(out, entry{path: p, rec: r})
	}
	return out
}

// Write records a failed create. Best-effort by design: a create that already
// failed must not fail differently because the graveyard is unwritable, so
// callers ignore the error and the user still sees the create's own message.
func Write(stateDir string, r Record) error {
	if err := os.MkdirAll(dir(stateDir), 0o700); err != nil {
		return err
	}
	if r.When.IsZero() {
		r.When = time.Now()
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p := file(stateDir, r.Name)
	for _, e := range entries(stateDir) {
		switch {
		case e.rec.Name == r.Name && e.path != p:
			// A stale alias for this same session, left by an older naming
			// scheme. One session, one row.
			os.Remove(e.path)
		case e.rec.Name != r.Name && e.path == p:
			// Our destination holds a different session's record: the old
			// scheme folded that name onto ours, so feat/login can be sitting
			// exactly where feat-login now belongs. Move it aside rather than
			// truncating someone else's failure.
			os.Rename(e.path, freePath(stateDir, e.rec.Name))
		}
	}
	return os.WriteFile(p, b, 0o600)
}

// List returns live tombstones, newest first, pruning expired ones as it
// goes. Unreadable or malformed files are skipped rather than fatal: a
// corrupt tombstone must never be able to break `pier ls`.
func List(stateDir string) []Record {
	var out []Record
	for _, e := range entries(stateDir) {
		if time.Since(e.rec.When) > retention {
			os.Remove(e.path)
			continue
		}
		out = append(out, e.rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out
}

// Dismiss forgets one record. It matches on the name stored inside each file
// rather than trusting a path built from the name, so a record written under
// an older naming scheme still clears — and, more importantly, dismissing
// feat/login can never delete feat-login's record instead. Removing an absent
// tombstone is success: the caller wanted it gone and it is.
func Dismiss(stateDir, name string) error {
	var firstErr error
	for _, e := range entries(stateDir) {
		if e.rec.Name != name {
			continue
		}
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

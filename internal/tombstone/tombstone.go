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
	"encoding/json"
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

// file keys the record by session name, matching how create names its log.
func file(stateDir, name string) string {
	return filepath.Join(dir(stateDir), strings.ReplaceAll(name, "/", "-")+".json")
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
	return os.WriteFile(file(stateDir, r.Name), b, 0o600)
}

// List returns live tombstones, newest first, pruning expired ones as it
// goes. Unreadable or malformed files are skipped rather than fatal: a
// corrupt tombstone must never be able to break `pier ls`.
func List(stateDir string) []Record {
	ents, err := os.ReadDir(dir(stateDir))
	if err != nil {
		return nil
	}
	var out []Record
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
		if time.Since(r.When) > retention {
			os.Remove(p)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out
}

// Dismiss forgets one record. Removing an absent tombstone is success — the
// caller wanted it gone and it is.
func Dismiss(stateDir, name string) error {
	err := os.Remove(file(stateDir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

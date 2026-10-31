package main

// The checkout's changes, pushed: while a frontend follows the "git"
// topic, the checkout is watched (inotify where there is one) and its
// changes against the base published whenever they differ — a frontend
// never polls git, and an idle checkout costs nothing.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier/gitview"
)

// gitState is what the "git" topic publishes.
type gitState struct {
	Changes *gitview.Changes `json:"changes,omitempty"`
	// Stamps tell a changed file's edits apart when its line counts stay
	// the same: its size and modification time in the working tree.
	Stamps map[string]string `json:"stamps,omitempty"`
	Error  string            `json:"error,omitempty"`
	Code   string            `json:"code,omitempty"`
	At     time.Time         `json:"at"`
}

// stamps are the changed files' stamps.
func stamps(dir string, c gitview.Changes) map[string]string {
	m := map[string]string{}
	for _, f := range c.Files {
		if fi, err := os.Lstat(filepath.Join(dir, f.Path)); err == nil {
			m[f.Path] = strconv.FormatInt(fi.Size(), 36) + "." + strconv.FormatInt(fi.ModTime().UnixNano(), 36)
		} else {
			m[f.Path] = "-"
		}
	}
	return m
}

const (
	// gitSettle is how long the checkout must stay quiet before a read;
	// gitMaxWait bounds the wait while it never does (a build writing).
	gitSettle  = 300 * time.Millisecond
	gitMaxWait = 2 * time.Second
	// gitSafety re-reads anyway, for what no watch sees (a full watch
	// list, a ref moved by another worktree).
	gitSafety = 30 * time.Second
	// gitLinger keeps watching this long after the last follower left, so
	// a reconnect doesn't start over.
	gitLinger = time.Minute
)

// pollEvery is how often a checkout with no watch is looked at; a var so
// tests look sooner.
var pollEvery = 3 * time.Second

func (h *hub) kickGit() {
	select {
	case h.gitKick <- struct{}{}:
	default:
	}
}

func (h *hub) setGitLive(live bool) {
	h.mu.Lock()
	h.gitLive = live
	h.mu.Unlock()
}

// watchGit keeps the "git" topic current while anyone follows it.
func (h *hub) watchGit() {
	for {
		// Nobody follows: wait until someone does.
		for !h.watching("git") {
			select {
			case <-h.gitKick:
			case <-time.After(time.Second):
			}
		}
		h.followCheckout()
	}
}

// followCheckout watches the checkout and publishes its changes until
// nobody has followed them for gitLinger.
func (h *hub) followCheckout() {
	dir, err := repoRoot()
	if err != nil {
		// No checkout yet (a session still bootstrapping): say so, and
		// look again shortly.
		h.publish("git", gitState{Error: err.Error(), Code: errCode(err), At: time.Now()})
		select {
		case <-h.gitKick:
		case <-time.After(5 * time.Second):
		}
		return
	}
	events := make(chan struct{}, 1)
	stop := watchTree(dir, func() {
		select {
		case events <- struct{}{}:
		default:
		}
	})
	defer stop()
	defer h.setGitLive(false)

	var last []byte
	read := func(force bool) {
		st := gitState{At: time.Now()}
		if c, err := readChanges(dir); err != nil {
			st.Error, st.Code = err.Error(), errCode(err)
		} else {
			st.Changes, st.Stamps = &c, stamps(dir, c)
		}
		// Only what changed goes out; the time doesn't count.
		b, _ := json.Marshal(gitState{Changes: st.Changes, Stamps: st.Stamps, Error: st.Error})
		if force || !bytes.Equal(b, last) {
			last = b
			h.publish("git", st)
		}
		h.setGitLive(true)
	}
	read(true)

	safety := time.NewTicker(gitSafety)
	defer safety.Stop()
	alone := time.Time{}
	for {
		select {
		case <-h.gitKick:
			read(true)
		case <-safety.C:
			read(false)
		case <-events:
			// Let a burst of writes settle, but not forever.
			first := time.Now()
			for quiet := false; !quiet && time.Since(first) < gitMaxWait; {
				select {
				case <-events:
				case <-time.After(gitSettle):
					quiet = true
				}
			}
			read(false)
		case <-time.After(5 * time.Second):
		}
		if h.watching("git") {
			alone = time.Time{}
		} else if alone.IsZero() {
			alone = time.Now()
		} else if time.Since(alone) > gitLinger {
			return
		}
	}
}

// pollTree stands in for a watch that can't be had: it says "maybe
// changed" every few seconds, and only real changes get published.
func pollTree(dir string, changed func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				changed()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

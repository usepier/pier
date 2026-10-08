package api

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// GitHub cadence: often while checks run, less when they've settled, much
// less while nobody looks — and never into the rate limit's last stretch.
const (
	prPending  = 30 * time.Second
	prFocused  = 60 * time.Second
	prHidden   = 5 * time.Minute
	prReserve  = 200 // requests kept back for actions
	prLogBytes = 64 << 10
)

// prCache is every session's pull request, refreshed along with the
// overview on its own cadence.
type prCache struct {
	s *Server

	mu      sync.Mutex
	prs     map[string]pier.PR // by session id
	at      time.Time
	pending bool
	lastErr error
}

func (c *prCache) snapshot() map[string]pier.PR {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]pier.PR, len(c.prs))
	for k, v := range c.prs {
		out[k] = v
	}
	return out
}

// kick makes the next overview round refresh pull requests (after a push,
// a merge, a re-run).
func (c *prCache) kick() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
	c.s.overview.kick(fastAfterAction)
}

func (c *prCache) due(focused bool, limit int, reset time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit > 0 && limit < prReserve && time.Now().Before(reset) {
		return false
	}
	every := prHidden
	switch {
	case c.pending:
		every = prPending
	case focused:
		every = prFocused
	}
	return time.Since(c.at) >= every
}

// refreshIfDue reads the pull requests when it's time; true when they
// changed.
func (c *prCache) refreshIfDue(ctx context.Context, be Backend, sessions []pier.Session, focused bool) bool {
	lim := be.GitHubLimit()
	if !c.due(focused, lim.Remaining, lim.ResetAt) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prs, err := be.PullRequests(ctx, sessions)
	c.mu.Lock()
	c.at, c.lastErr = time.Now(), err
	if err != nil && len(prs) == 0 {
		c.mu.Unlock()
		return false
	}
	old := c.prs
	c.prs = prs
	c.pending = false
	for _, p := range prs {
		if p.ChecksState() == "pending" && (p.State == "open" || p.State == "draft") {
			c.pending = true
		}
	}
	c.mu.Unlock()
	for _, s := range sessions {
		c.s.notices.pr(s, old[s.ID], prs[s.ID])
	}
	return !reflect.DeepEqual(old, prs)
}

// cached is a session's pull request from the last refresh.
func (c *prCache) cached(id string) (pier.PR, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.prs[id]
	return p, ok
}

// prFor finds a session's pull request: the cache, else a fresh look.
func (s *Server) prFor(ctx context.Context, be Backend, sess pier.Session) (pier.PR, error) {
	if p, ok := s.prs.cached(sess.ID); ok {
		return p, nil
	}
	prs, err := be.PullRequests(ctx, []pier.Session{sess})
	if p, ok := prs[sess.ID]; ok {
		return p, nil
	}
	if err != nil {
		return pier.PR{}, err
	}
	return pier.PR{}, pier.ErrNoPR
}

func wirePRDetail(d pier.PRDetail) wire.PRDetail {
	w := wire.PRDetail{PRSummary: *wirePR(d.PR), ID: d.ID, Body: d.Body, Runs: []wire.PRCheck{}, Threads: []wire.PRThread{}, Reviews: []wire.PRReview{}}
	for _, c := range d.Checks {
		w.Runs = append(w.Runs, wire.PRCheck{Name: c.Name, State: c.State, URL: c.URL, JobID: c.JobID, RunID: c.RunID})
	}
	for _, t := range d.Threads {
		wt := wire.PRThread{Path: t.Path, Line: t.Line, Resolved: t.Resolved, Outdated: t.Outdated, Comments: []wire.PRComment{}}
		for _, c := range t.Comments {
			wt.Comments = append(wt.Comments, wire.PRComment{Author: c.Author, Body: c.Body})
		}
		w.Threads = append(w.Threads, wt)
	}
	for _, r := range d.Reviews {
		w.Reviews = append(w.Reviews, wire.PRReview{Author: r.Author, State: r.State, Body: r.Body})
	}
	return w
}

const createPRPrompt = "Open a pull request for this branch: commit anything outstanding, push, then create it with `gh pr create` — a clear title, and a body that sums up what changed and why."

// compose writes a prompt for the agent from the pull request's state.
func compose(ctx context.Context, be Backend, sess pier.Session, d pier.PRDetail, kind string) (string, error) {
	switch kind {
	case "createPR":
		return createPRPrompt, nil
	case "fixCI":
		var b strings.Builder
		fmt.Fprintf(&b, "CI is failing on PR #%d. Find out why, fix it, and push.\n", d.Number)
		logs := 0
		for _, c := range d.Checks {
			if c.State != "failing" {
				continue
			}
			fmt.Fprintf(&b, "\nFailing: %s", c.Name)
			if c.JobID == 0 || logs == 2 {
				b.WriteString("\n")
				continue
			}
			log, err := be.CheckLog(ctx, sess, c.JobID, 6000)
			if err != nil || strings.TrimSpace(log) == "" {
				b.WriteString("\n")
				continue
			}
			logs++
			fmt.Fprintf(&b, " — the end of its log:\n```\n%s\n```\n", strings.TrimSpace(log))
		}
		return b.String(), nil
	case "addressComments":
		var b strings.Builder
		fmt.Fprintf(&b, "Address the review feedback on PR #%d, then push. Reply to nothing — just make the changes.\n", d.Number)
		for _, r := range d.Reviews {
			if r.State == "changes_requested" && strings.TrimSpace(r.Body) != "" {
				fmt.Fprintf(&b, "\n%s asked for changes:\n%s\n", r.Author, strings.TrimSpace(r.Body))
			}
		}
		threads := append(d.Threads[:0:0], d.Threads...)
		sort.SliceStable(threads, func(i, j int) bool { return threads[i].Path < threads[j].Path })
		n := 0
		for _, t := range threads {
			if t.Resolved || len(t.Comments) == 0 {
				continue
			}
			n++
			where := t.Path
			if t.Line > 0 {
				where = fmt.Sprintf("%s:%d", t.Path, t.Line)
			}
			fmt.Fprintf(&b, "\n%s\n", where)
			for _, c := range t.Comments {
				fmt.Fprintf(&b, "%s: %s\n", c.Author, strings.TrimSpace(c.Body))
			}
		}
		if n == 0 && !strings.Contains(b.String(), "asked for changes") {
			return "", errors.New("there's no open review feedback on this pull request")
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unknown prompt %q", kind)
}

func init() {
	handle("pr.get", "A session's pull request in full: checks, review threads, reviews.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (wire.PRDetail, error) {
			be, sess, err := s.sessionFor(ctx, p.ID)
			if err != nil {
				return wire.PRDetail{}, err
			}
			pr, err := s.prFor(ctx, be, sess)
			if err != nil {
				return wire.PRDetail{}, err
			}
			d, err := be.PullRequest(ctx, sess, pr.Number)
			return wirePRDetail(d), err
		})
	handle("pr.log", "The end of a check's log (GitHub Actions).",
		func(ctx context.Context, s *Server, _ *Call, p wire.PRLogParams) (wire.LogText, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.LogText{}, err
			}
			text, err := be.CheckLog(ctx, sess, p.JobID, prLogBytes)
			return wire.LogText{Text: text}, err
		})
	handle("pr.rerun", "Re-runs a workflow run's failed jobs.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PRRerunParams) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return nil, err
			}
			if err := be.RerunChecks(ctx, sess, p.RunID); err != nil {
				return nil, err
			}
			s.prs.kick()
			return nil, nil
		})
	handle("pr.merge", "Merges a session's pull request — only at the head that was reviewed (kind head_moved otherwise).",
		func(ctx context.Context, s *Server, _ *Call, p wire.PRMergeParams) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return nil, err
			}
			pr, err := s.prFor(ctx, be, sess)
			if err != nil {
				return nil, err
			}
			if err := be.MergePR(ctx, sess, pr.ID, p.Method, p.ExpectedHeadOid); err != nil {
				return nil, err
			}
			s.prs.kick()
			return nil, nil
		})
	handle("chat.compose", "A prompt for the agent from the pull request — fixCI (with the failing logs), addressComments, createPR — for the composer, not sent.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ComposeParams) (wire.Composed, error) {
			if p.Kind == "createPR" {
				return wire.Composed{Text: createPRPrompt}, nil
			}
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.Composed{}, err
			}
			pr, err := s.prFor(ctx, be, sess)
			if err != nil {
				return wire.Composed{}, err
			}
			d, err := be.PullRequest(ctx, sess, pr.Number)
			if err != nil {
				return wire.Composed{}, err
			}
			text, err := compose(ctx, be, sess, d, p.Kind)
			return wire.Composed{Text: text}, err
		})
	handle("sessions.removeMerged", "Removes a session whose pull request merged — only when nothing in it is left behind: all committed, and the merged head is its head.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			pr, err := s.prFor(ctx, be, sess)
			if err != nil {
				return nil, err
			}
			if err := mergedAndClean(ctx, be, sess, pr); err != nil {
				return nil, err
			}
			if err := be.Remove(ctx, sess); err != nil {
				return nil, err
			}
			s.overview.kick(fastAfterAction)
			return nil, nil
		})
}

// mergedAndClean is the guard on removing a merged session: its pull
// request merged at the head the checkout has, with nothing uncommitted —
// read live when it runs, else from the last look.
func mergedAndClean(ctx context.Context, be Backend, sess pier.Session, pr pier.PR) error {
	if pr.State != "merged" {
		return fmt.Errorf("%s's pull request hasn't merged", sess.Name)
	}
	ch, _, ok := pier.CachedChanges(sess.ID)
	if readable(be, sess) {
		live, err := be.Changes(ctx, sess)
		if err != nil {
			return err
		}
		ch, ok = live, true
	}
	switch {
	case !ok:
		return fmt.Errorf("%s's checkout hasn't been read on this Mac — wake it to check nothing is left behind", sess.Name)
	case ch.Uncommitted() > 0:
		return fmt.Errorf("%s has uncommitted changes", sess.Name)
	case ch.Branch.Oid != pr.HeadOid:
		return fmt.Errorf("%s has commits the merged pull request doesn't", sess.Name)
	}
	return nil
}

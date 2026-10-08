package api

import (
	"bytes"
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/gitview"
	"github.com/usepier/pier/pkg/pier/wire"
)

func wireChanges(c gitview.Changes) wire.Changes {
	w := wire.Changes{
		Branch: c.Branch.Head, Head: c.Branch.Oid, Upstream: c.Branch.Upstream, Ahead: c.Branch.Ahead, Behind: c.Branch.Behind,
		BaseRef: c.BaseRef, BaseOid: c.BaseOid, Files: []wire.ChangedFile{}, Uncommitted: c.Uncommitted(),
	}
	for _, f := range c.Files {
		w.Files = append(w.Files, wire.ChangedFile{Path: f.Path, OldPath: f.OldPath, Status: string(f.Status), Added: f.Added,
			Removed: f.Removed, Binary: f.Binary, Uncommitted: f.Uncommitted})
	}
	return w
}

// readable reports a session whose checkout can be read now.
func readable(be Backend, s pier.Session) bool { return running(s) && be.HasKey(s) }

// unreadable says why a session's checkout can't be read now.
func unreadable(be Backend, s pier.Session) error {
	switch {
	case s.State == pier.StateParked:
		return fmt.Errorf("%w: %s — wake it to read it", pier.ErrParked, s.Name)
	case !be.HasKey(s):
		return fmt.Errorf("%w: %s", pier.ErrNoKey, s.Name)
	}
	if err := pier.CheckReady(s); err != nil {
		return err
	}
	return fmt.Errorf("%s isn't running", s.Name)
}

func asOf(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}

func init() {
	handle("git.changes", "Everything a session's branch changed against its base, committed or not. A parked session answers with what this machine last read.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (wire.Changes, error) {
			be, sess, err := s.sessionFor(ctx, p.ID)
			if err != nil {
				return wire.Changes{}, err
			}
			if !readable(be, sess) {
				if c, at, ok := pier.CachedChanges(sess.ID); ok {
					w := wireChanges(c)
					w.AsOf = asOf(at)
					return w, nil
				}
				return wire.Changes{}, unreadable(be, sess)
			}
			c, err := be.Changes(ctx, sess)
			if err != nil {
				return wire.Changes{}, err
			}
			w := wireChanges(c)
			w.Live = true
			return w, nil
		})
	handle("git.diff", "One changed file's unified diff against the base.",
		func(ctx context.Context, s *Server, _ *Call, p wire.DiffParams) (wire.FileDiff, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.FileDiff{}, err
			}
			if !readable(be, sess) {
				if patch, at, ok := pier.CachedDiff(sess.ID, p.Path); ok {
					return wire.FileDiff{Path: p.Path, Patch: patch, AsOf: asOf(at)}, nil
				}
				return wire.FileDiff{}, unreadable(be, sess)
			}
			f := gitview.File{Path: p.Path, OldPath: p.OldPath, Status: gitview.Status(p.Status)}
			patch, truncated, err := be.Diff(ctx, sess, f, p.Context)
			return wire.FileDiff{Path: p.Path, Patch: patch, Truncated: truncated, Live: true}, err
		})
	handle("files.read", "A file of a session's checkout, with the sha files.write checks against.",
		func(ctx context.Context, s *Server, _ *Call, p wire.FilePathParams) (wire.FileContent, error) {
			var fc pier.FileContent
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				fc, err = be.ReadFile(ctx, sess, p.Path)
				return err
			})
			if err != nil {
				return wire.FileContent{}, err
			}
			w := wire.FileContent{Path: p.Path, Size: fc.Size, Sha: fc.Sha, Missing: fc.Missing, TooLarge: fc.TooLarge}
			if utf8.Valid(fc.Data) && !bytes.ContainsRune(fc.Data, 0) {
				w.Text = string(fc.Data)
			} else {
				w.Binary = true
			}
			return w, nil
		})
	handle("files.write", "Saves a file of a session's checkout, once per opId, unless it changed there since it was read (kind conflict).",
		func(ctx context.Context, s *Server, _ *Call, p wire.FileWriteParams) (wire.FileWritten, error) {
			var sha string
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				sha, err = be.WriteFile(ctx, sess, p.OpID, p.Path, []byte(p.Text), p.BaseSha)
				return err
			})
			return wire.FileWritten{Sha: sha}, err
		})
	handle("files.search", "Finds text in a session's checkout: literal by default, or an extended regex.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SearchParams) (wire.SearchResult, error) {
			out := wire.SearchResult{Matches: []wire.SearchMatch{}}
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				ms, truncated, err := be.Search(ctx, sess, p.Query, p.Regex, p.CaseSensitive)
				for _, m := range ms {
					out.Matches = append(out.Matches, wire.SearchMatch{Path: m.Path, Line: m.Line, Col: m.Col, Text: m.Text})
				}
				out.Truncated = truncated
				return err
			})
			return out, err
		})
	handle("git.commit", "Commits everything in a session's checkout, once per opId.",
		func(ctx context.Context, s *Server, _ *Call, p wire.CommitParams) (wire.CommitResult, error) {
			var head string
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) (err error) {
				head, err = be.Commit(ctx, sess, p.OpID, p.Message)
				return err
			})
			return wire.CommitResult{Head: head}, err
		})
	handle("git.push", "Pushes a session's branch to origin, once per opId.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PushParams) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.Push(ctx, sess, p.OpID)
			})
		})
}

package api

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func wireJob(j pier.Job) wire.Job {
	w := wire.Job{
		ID: j.ID, Kind: wire.JobKind(j.Kind), Repo: j.Repo, Session: j.Session,
		State: wire.JobState(j.State), StartedAt: j.Started.UTC(), EndedAt: wire.TimeOrNil(j.Ended),
		Error: j.Error, Summary: j.Summary, Log: j.Log,
	}
	if w.State == "" {
		w.State = wire.JobRunning
	}
	return w
}

func msPtr(ms *int64) *time.Duration {
	if ms == nil {
		return nil
	}
	d := time.Duration(*ms) * time.Millisecond
	return &d
}

func init() {
	handle("sessions.create", "Starts a session from a linked checkout's HEAD (uncommitted edits travel too) in a background job. The row lists as starting until it is ready.",
		func(ctx context.Context, s *Server, _ *Call, p wire.CreateParams) (wire.Job, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Job{}, err
			}
			root, err := pier.RepoRoot(p.Path)
			if err != nil {
				return wire.Job{}, err
			}
			if strings.TrimSpace(p.Branch) == "" {
				return wire.Job{}, invalidParams("branch is required")
			}
			req := pier.CreateRequest{
				RepoRoot: root, Branch: p.Branch, Base: p.Base,
				Idle: msPtr(p.IdleMs), Cap: msPtr(p.CapMs), NoPool: p.NoPool,
			}
			if a := p.Agent; a != nil {
				if a.Kind != "claude" && a.Kind != "codex" {
					return wire.Job{}, invalidParams("agent is claude or codex")
				}
				req.Agent = &pier.AgentRequest{Kind: a.Kind, Model: a.Model, PermissionMode: a.PermissionMode, Prompt: a.Prompt}
			}
			j, err := be.StartCreate(req)
			if err != nil {
				return wire.Job{}, err
			}
			s.overview.kick(fastAfterAction)
			return wireJob(j), nil
		})
	handle("image.bake", "Bakes the checkout's repo into a session image in a background job.",
		func(ctx context.Context, s *Server, _ *Call, p wire.BakeParams) (wire.Job, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Job{}, err
			}
			root, err := pier.RepoRoot(p.Path)
			if err != nil {
				return wire.Job{}, err
			}
			j, err := be.StartBake(root, p.IncludeRepo)
			if err != nil {
				return wire.Job{}, err
			}
			s.overview.kick(fastAfterAction)
			return wireJob(j), nil
		})
	handle("pool.fill", "Tops up the checkout's repo pool now, in a background job.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PathParams) (wire.Job, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Job{}, err
			}
			root, err := pier.RepoRoot(p.Path)
			if err != nil {
				return wire.Job{}, err
			}
			j, err := be.StartFill(root)
			if err != nil {
				return wire.Job{}, err
			}
			s.overview.kick(fastAfterAction)
			return wireJob(j), nil
		})
	handle("jobs.list", "Background jobs this machine started, newest first (finished ones stay a day).",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) ([]wire.Job, error) {
			out := []wire.Job{}
			for _, j := range pier.Jobs() {
				out = append(out, wireJob(j))
			}
			return out, nil
		})
	handle("jobs.cancel", "Stops a running job the way ctrl-c would, cleanup included.",
		func(ctx context.Context, s *Server, _ *Call, p wire.JobRef) (*wire.Empty, error) {
			return nil, pier.CancelJob(p.ID)
		})
	handle("jobs.log", "The tail of a job's human log.",
		func(ctx context.Context, s *Server, _ *Call, p wire.LogParams) (wire.LogText, error) {
			j, err := pier.JobByID(p.ID)
			if err != nil {
				return wire.LogText{}, err
			}
			return tailFile(j.Log, p.Lines)
		})
	watch("job", "A job's progress: every event so far, then each new one, ending with its done or failed event.",
		func(ctx context.Context, s *Server, p wire.JobRef, sub *Sub[wire.JobUpdate]) (wire.JobUpdate, error) {
			j, err := pier.JobByID(p.ID)
			if err != nil {
				return wire.JobUpdate{}, err
			}
			events, off := pier.JobEvents(j)
			seen := make([]wire.Event, 0, len(events))
			for _, e := range events {
				seen = append(seen, wireEvent(e))
			}
			if j.State == pier.JobRunning {
				go pier.FollowJob(ctx, p.ID, off, func(e pier.Event) {
					cur, _ := pier.JobByID(p.ID)
					sub.Emit(wire.JobUpdate{Job: wireJob(cur), Events: []wire.Event{wireEvent(e)}})
				})
			}
			return wire.JobUpdate{Job: wireJob(j), Events: seen}, nil
		})
}

// tailFile returns the last lines of a text file.
func tailFile(path string, lines int) (wire.LogText, error) {
	if lines <= 0 {
		lines = 500
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return wire.LogText{}, nil
		}
		return wire.LogText{}, err
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := wire.LogText{}
	if len(all) > lines {
		all, out.Truncated = all[len(all)-lines:], true
	}
	out.Text = strings.Join(all, "\n")
	return out, nil
}

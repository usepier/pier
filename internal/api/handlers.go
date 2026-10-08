package api

import (
	"context"
	"strings"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func init() {
	handle("initialize", "Opens the session: the protocol version both sides speak, and whether pier is set up.",
		func(ctx context.Context, s *Server, _ *Call, p wire.InitializeParams) (wire.InitializeResult, error) {
			r := wire.InitializeResult{Protocol: wire.ProtocolVersion, Version: s.opts.Version}
			if be, err := s.backend(); err == nil {
				r.Configured, r.Cloud = true, be.Cloud()
			}
			return r, nil
		})
	handle("shutdown", "Ends the server once every earlier request has answered and this reply is out.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (any, error) {
			s.settle(exitGrace)
			return afterReply{result: nil, then: func() {
				if s.opts.Exit != nil {
					s.opts.Exit()
				}
			}}, nil
		})

	// --- sessions ---------------------------------------------------------

	handle("sessions.resume", "Wakes a parked session and waits until it answers.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) error { return be.Resume(ctx, sess) })
		})
	handle("sessions.keep", "Never parks this session when idle (it still stops on the runaway cap).",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) error { return be.Keep(ctx, sess) })
		})
	handle("sessions.remove", "Destroys a session and its disk, or dismisses a failed create.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) (*wire.Empty, error) {
			return nil, s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) error { return be.Remove(ctx, sess) })
		})
	handle("sessions.resize", "Changes a session's machine type in a background job: one park and resume, disk intact.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ResizeParams) (wire.Job, error) {
			var job pier.Job
			err := s.withSession(ctx, p.ID, func(be Backend, sess pier.Session) (err error) {
				job, err = be.StartResize(sess, p.MachineType)
				return err
			})
			return wireJob(job), err
		})
	handle("sessions.machines", "The machine types a session can resize to: same CPU architecture, with shape and cost.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) ([]wire.Machine, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			sess, err := s.overview.session(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			return wireMachines(be.Machines(sess)), nil
		})

	// --- repos, pools -----------------------------------------------------

	handle("repos.link", "Links the repository containing path as a local checkout to act from.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PathParams) (wire.Checkout, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Checkout{}, err
			}
			co, err := be.LinkCheckout(p.Path)
			if err != nil {
				return wire.Checkout{}, err
			}
			s.overview.kick(0)
			return wireCheckout(co), nil
		})
	handle("repos.unlink", "Forgets a linked checkout; the clone itself is untouched.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PathParams) (*wire.Empty, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			if err := be.UnlinkCheckout(p.Path); err != nil {
				return nil, err
			}
			s.overview.kick(0)
			return nil, nil
		})
	handle("repos.inspect", "What a create from the checkout containing path would carry: branch, HEAD, uncommitted changes, base.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PathParams) (wire.CheckoutInfo, error) {
			info, err := pier.InspectCheckout(p.Path)
			if err != nil {
				return wire.CheckoutInfo{}, err
			}
			return wire.CheckoutInfo{
				Checkout: wireCheckout(info.Checkout),
				Branch:   info.Branch, Head: info.Head, Dirty: info.Dirty, Base: info.Base,
			}, nil
		})
	handle("pool.setSize", "Sets how many pooled sessions the checkout's repo keeps (0 turns the pool off and drains it).",
		func(ctx context.Context, s *Server, c *Call, p wire.PoolSizeParams) (*wire.Empty, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			root, err := pier.RepoRoot(p.Path)
			if err != nil {
				return nil, err
			}
			if _, err := be.SetPoolSize(ctx, root, p.Size, progress(c)); err != nil {
				return nil, err
			}
			s.overview.kick(fastAfterAction)
			return nil, nil
		})
	handle("pool.drain", "Destroys a repo's pooled sessions.",
		func(ctx context.Context, s *Server, c *Call, p wire.RepoRef) (wire.DrainResult, error) {
			be, err := s.backend()
			if err != nil {
				return wire.DrainResult{}, err
			}
			n, err := be.DrainPool(ctx, p.Repo, progress(c))
			s.overview.kick(fastAfterAction)
			return wire.DrainResult{Removed: n}, err
		})

	// --- account, settings ------------------------------------------------

	handle("account.headroom", "The account's vCPU quota use in the active region.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.Quota, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Quota{}, err
			}
			q, err := be.Headroom(ctx)
			return wire.Quota{Used: q.Used, Limit: q.Limit, Detail: q.Detail}, err
		})
	handle("doctor.run", "Checks the environment and the cloud account.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) ([]wire.Check, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			out := []wire.Check{}
			for _, ch := range be.Doctor(ctx) {
				st := wire.CheckOK
				if !ch.OK {
					st = wire.CheckFail
				}
				out = append(out, wire.Check{Name: ch.Name, Status: st, Detail: ch.Detail})
			}
			return out, nil
		})
	handle("settings.get", "The settings page: groups, fields and current values for the active cloud.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.Settings, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Settings{}, err
			}
			return wireSettings(be), nil
		})
	handle("settings.set", "Validates and saves one setting, returning the page as it now reads.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SettingParams) (wire.Settings, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Settings{}, err
			}
			if err := be.Set(p.Key, p.Value); err != nil {
				return wire.Settings{}, invalidParams(err.Error())
			}
			// A different cloud, account or region needs a new client.
			if p.Key == "driver" || strings.HasPrefix(p.Key, "aws.") || strings.HasPrefix(p.Key, "gcp.") {
				s.reopen()
				if be, err = s.backend(); err != nil {
					return wire.Settings{}, err
				}
			}
			return wireSettings(be), nil
		})
	handle("settings.machines", "The machine catalog the settings picker offers, around a current type.",
		func(ctx context.Context, s *Server, _ *Call, p wire.MachinesParams) ([]wire.Machine, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			return wireMachines(be.MachineCatalog(p.Current)), nil
		})
}

// withSession runs an action on a session by id, then refreshes the list
// briskly until its effect shows.
func (s *Server) withSession(ctx context.Context, id string, act func(Backend, pier.Session) error) error {
	be, err := s.backend()
	if err != nil {
		return err
	}
	sess, err := s.overview.session(ctx, id)
	if err != nil {
		return err
	}
	err = act(be, sess)
	s.overview.kick(fastAfterAction)
	return err
}

// progress forwards a pier progress callback as $/progress notifications.
func progress(c *Call) pier.Progress {
	return func(e pier.Event) { c.Progress(wireEvent(e)) }
}

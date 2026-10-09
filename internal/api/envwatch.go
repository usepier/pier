package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func wireEnv(e pier.Env) wire.Env {
	w := wire.Env{Windows: wireWindows(e.Windows), Containers: []wire.Container{}, Processes: []wire.Process{}, Ports: []wire.Port{}, At: e.At.UTC()}
	for _, c := range e.Containers {
		w.Containers = append(w.Containers, wire.Container{ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Status: c.Status, Ports: c.Ports})
	}
	for _, p := range e.Processes {
		w.Processes = append(w.Processes, wire.Process{PID: p.PID, Command: p.Command, CPU: p.CPU, RSS: p.RSS})
	}
	for _, p := range e.Ports {
		w.Ports = append(w.Ports, wire.Port{Port: p.Port, Process: p.Process, PID: p.PID})
	}
	if r := e.Res; r != nil {
		w.Resources = &wire.Resources{CPU: r.CPU, CPUs: r.CPUs, MemUsed: r.MemUsed, MemTotal: r.MemTotal, DiskUsed: r.DiskUsed, DiskTotal: r.DiskTotal, Load1: r.Load1}
	}
	return w
}

func init() {
	watch("env", "What runs in a session — windows, containers, processes, ports, load — pushed every few seconds while watched.",
		func(ctx context.Context, s *Server, p wire.SessionRef, sub *Sub[wire.Env]) (wire.Env, error) {
			be, sess, err := s.sessionFor(ctx, p.ID)
			if err != nil {
				return wire.Env{}, err
			}
			first := make(chan wire.Env, 1)
			firstErr := make(chan error, 1)
			go func() {
				sent := false
				for attempt := 0; ; attempt++ {
					err := be.Follow(ctx, sess, []string{"env"}, func(ev string, data json.RawMessage) {
						if ev != "env" {
							return
						}
						e, err := pier.ParseEnv(data)
						if err != nil {
							s.logf("environment of %s: %v", sess.Name, err)
							return
						}
						if !sent {
							sent = true
							first <- wireEnv(e)
							return
						}
						sub.Emit(wireEnv(e))
					})
					if ctx.Err() != nil {
						return
					}
					if !sent {
						firstErr <- err
						return
					}
					// The connection dropped mid-watch: try again, gently.
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Duration(min(attempt+1, 6)) * 5 * time.Second):
					}
				}
			}()
			select {
			case e := <-first:
				return e, nil
			case err := <-firstErr:
				return wire.Env{}, err
			case <-time.After(45 * time.Second):
				return wire.Env{}, errUnreachable
			case <-ctx.Done():
				return wire.Env{}, ctx.Err()
			}
		})
	handle("containers.action", "Starts, stops or restarts one of a session's containers.",
		func(ctx context.Context, s *Server, _ *Call, p wire.ContainerParams) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return nil, err
			}
			return nil, be.ContainerAction(ctx, sess, p.Container, p.Action)
		})
}

var errUnreachable = rpcErr(codeFailed, wire.ErrUnreachable, "the session didn't answer")

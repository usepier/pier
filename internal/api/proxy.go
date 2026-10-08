package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// The local proxy runs inside pier api, on the connections it keeps: one
// per session, shared with terminals and streams.

type proxyRunner struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	state     wire.ProxyState
	caPath    string
	listeners map[int]func(wire.ProxyState)
	nextL     int
}

func (p *proxyRunner) snapshot() wire.ProxyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state
	st.Sessions = append([]wire.ProxySession{}, st.Sessions...)
	return st
}

func (p *proxyRunner) set(f func(*wire.ProxyState)) {
	p.mu.Lock()
	f(&p.state)
	st := p.state
	fns := make([]func(wire.ProxyState), 0, len(p.listeners))
	for _, fn := range p.listeners {
		fns = append(fns, fn)
	}
	p.mu.Unlock()
	for _, fn := range fns {
		fn(st)
	}
}

func (p *proxyRunner) subscribe(fn func(wire.ProxyState)) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listeners == nil {
		p.listeners = map[int]func(wire.ProxyState){}
	}
	p.nextL++
	id := p.nextL
	p.listeners[id] = fn
	return func() {
		p.mu.Lock()
		delete(p.listeners, id)
		p.mu.Unlock()
	}
}

func wireProxy(st pier.ProxyStatus) []wire.ProxySession {
	out := []wire.ProxySession{}
	for _, s := range st.Sessions {
		ws := wire.ProxySession{ID: s.ID, Host: s.Name + ".pier", Ports: s.Ports, Localhost: s.Localhost}
		if ws.Ports == nil {
			ws.Ports = []int{}
		}
		if ws.Localhost == nil {
			ws.Localhost = []int{}
		}
		out = append(out, ws)
	}
	return out
}

// start runs the proxy and returns once it serves (or failed to).
func (s *Server) startProxy(ctx context.Context) error {
	p := &s.proxy
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return nil
	}
	be, err := s.backend()
	if err != nil {
		p.mu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(s.root)
	done := make(chan struct{})
	p.cancel, p.done = cancel, done
	p.mu.Unlock()

	up := make(chan error, 1)
	var once sync.Once
	go func() {
		defer close(done)
		err := be.RunProxy(runCtx, func(script string) error {
			return pier.AdminShell(script, "pier gives your sessions local names (<session>.pier) and mirrors their ports on this Mac.")
		}, func(st pier.ProxyStatus) {
			once.Do(func() { up <- nil })
			p.mu.Lock()
			p.caPath = st.CAPath
			p.mu.Unlock()
			p.set(func(w *wire.ProxyState) {
				w.Running, w.Error, w.CATrusted, w.Sessions = true, "", st.CATrusted, wireProxy(st)
			})
		})
		once.Do(func() { up <- err })
		p.mu.Lock()
		p.cancel, p.done = nil, nil
		p.mu.Unlock()
		msg := ""
		if err != nil && runCtx.Err() == nil {
			msg = err.Error()
		}
		p.set(func(w *wire.ProxyState) { *w = wire.ProxyState{Sessions: []wire.ProxySession{}, Error: msg} })
	}()
	select {
	case err := <-up:
		if err != nil {
			cancel()
		}
		return err
	case <-ctx.Done():
		// The caller gave up (the admin prompt is still up); the proxy
		// carries on and reports through proxy.watch.
		return nil
	case <-time.After(3 * time.Minute):
		return nil
	}
}

func (s *Server) stopProxy() {
	p := &s.proxy
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// forwards are the ports forwarded by hand, per session.
type forwards struct {
	mu   sync.Mutex
	list []wire.PortForward
}

func init() {
	handle("proxy.start", "Serves every running session as <session>.pier with its ports mirrored on this Mac. The first time, macOS asks for an administrator's password (local names and addresses).",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.ProxyState, error) {
			if err := s.startProxy(ctx); err != nil {
				if errors.Is(err, pier.ErrAdminCancelled) {
					return s.proxy.snapshot(), rpcErr(codeFailed, wire.ErrCancelled, err.Error())
				}
				return s.proxy.snapshot(), err
			}
			return s.proxy.snapshot(), nil
		})
	handle("proxy.stop", "Stops the proxy; sessions' names and mirrored ports go away.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (*wire.Empty, error) {
			s.stopProxy()
			return nil, nil
		})
	watch("proxy", "The proxy's state: running, each session's name and mirrored ports.",
		func(ctx context.Context, s *Server, _ wire.Empty, sub *Sub[wire.ProxyState]) (wire.ProxyState, error) {
			snap := s.proxy.snapshot()
			if snap.Sessions == nil {
				snap.Sessions = []wire.ProxySession{}
			}
			off := s.proxy.subscribe(sub.Emit)
			go func() {
				<-sub.Done()
				off()
			}()
			return snap, nil
		})
	handle("proxy.trustCA", "Makes this Mac trust the proxy's certificate, so https://<session>.pier opens without a warning (macOS asks for the password).",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (*wire.Empty, error) {
			s.proxy.mu.Lock()
			ca := s.proxy.caPath
			s.proxy.mu.Unlock()
			if ca == "" {
				return nil, invalidParams("the proxy isn't running")
			}
			if err := pier.TrustProxyCA(ca); err != nil {
				return nil, err
			}
			s.proxy.set(func(w *wire.ProxyState) { w.CATrusted = true })
			return nil, nil
		})

	handle("ports.forward", "Forwards a session's port to 127.0.0.1 on this Mac for as long as pier runs.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PortParams) (*wire.Empty, error) {
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.ForwardPort(ctx, sess, p.Local, p.Remote)
			})
			if err != nil {
				return nil, err
			}
			s.ports.mu.Lock()
			s.ports.list = append(s.ports.list, wire.PortForward(p))
			s.ports.mu.Unlock()
			return nil, nil
		})
	handle("ports.unforward", "Stops a port forward.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PortParams) (*wire.Empty, error) {
			err := s.withSession(ctx, p.SessionID, func(be Backend, sess pier.Session) error {
				return be.UnforwardPort(sess, p.Local, p.Remote)
			})
			s.ports.mu.Lock()
			kept := s.ports.list[:0]
			for _, f := range s.ports.list {
				if f != wire.PortForward(p) {
					kept = append(kept, f)
				}
			}
			s.ports.list = kept
			s.ports.mu.Unlock()
			return nil, err
		})
	handle("ports.list", "The ports forwarded by hand.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) ([]wire.PortForward, error) {
			s.ports.mu.Lock()
			defer s.ports.mu.Unlock()
			return append([]wire.PortForward{}, s.ports.list...), nil
		})

	handle("cli.status", "Whether the pier command in the shell is this app's pier.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.CLIStatus, error) {
			self, err := selfPath()
			if err != nil {
				return wire.CLIStatus{}, err
			}
			st := pier.CLIInstallStatus(self)
			return wire.CLIStatus{Link: st.Link, Installed: st.Installed, Other: st.Other}, nil
		})
	handle("cli.install", "Installs the pier command (a link to this app's pier); macOS asks for an administrator's password if needed.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.CLIStatus, error) {
			self, err := selfPath()
			if err != nil {
				return wire.CLIStatus{}, err
			}
			if err := pier.InstallCLI(self); err != nil {
				if errors.Is(err, pier.ErrAdminCancelled) {
					return wire.CLIStatus{}, rpcErr(codeFailed, wire.ErrCancelled, err.Error())
				}
				return wire.CLIStatus{}, err
			}
			st := pier.CLIInstallStatus(self)
			return wire.CLIStatus{Link: st.Link, Installed: st.Installed, Other: st.Other}, nil
		})
}

// selfPath is this pier binary, links resolved.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

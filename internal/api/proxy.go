package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/proxy"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// The local proxy is one process per machine (internal/proxy): pier api
// starts it in the background when asked, follows its state wherever it
// came from (the app, `pier proxy`), and leaves it running when the app
// quits.

type proxyRunner struct {
	mu        sync.Mutex
	following bool
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

func wireProxy(st proxy.Status) []wire.ProxySession {
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

// ProxyControl is the machine's proxy, as pier api drives it.
type ProxyControl interface {
	// Start puts the network setup in place (asking for an
	// administrator's password the first time) and starts the proxy in
	// the background, unless one runs.
	Start(ctx context.Context) error
	// Stop stops the proxy, however it was started (proxy.ErrNotRunning
	// when none runs).
	Stop() error
	// Follow sends the running proxy's state as it changes, until it
	// stops (nil) or ctx ends; proxy.ErrNotRunning when none runs.
	Follow(ctx context.Context, fn func(proxy.Status)) error
	// Forget stops it and takes its network setup away (the resolver,
	// the boot task, the addresses), asking for a password.
	Forget() error
}

// machineProxy is the real one: internal/proxy's background process.
type machineProxy struct{}

func (machineProxy) Start(ctx context.Context) error {
	err := proxy.EnsureNet(nil, func(script string) error {
		return pier.AdminShell(script, "pier gives your sessions local names (<session>.pier) and mirrors their ports on this Mac.")
	})
	if err != nil {
		return err
	}
	self, err := selfPath()
	if err != nil {
		return err
	}
	return proxy.StartBackground(config.Dir(), []string{self, "proxy", "serve"})
}

func (machineProxy) Stop() error { return proxy.Stop(config.Dir()) }

func (machineProxy) Follow(ctx context.Context, fn func(proxy.Status)) error {
	return proxy.Follow(ctx, config.Dir(), fn)
}

func (m machineProxy) Forget() error {
	if err := m.Stop(); err != nil && !errors.Is(err, proxy.ErrNotRunning) {
		return err
	}
	return proxy.ForgetNet(func(script string) error {
		return pier.AdminShell(script, "pier removes the local names it set up for your sessions.")
	})
}

// proxyRecheck is how often a proxy that isn't running is looked for.
var proxyRecheck = 2 * time.Second

// followProxy keeps the proxy's state current from whichever proxy runs
// on this machine, for as long as pier api does.
func (s *Server) followProxy() {
	p := &s.proxy
	p.mu.Lock()
	if p.following {
		p.mu.Unlock()
		return
	}
	p.following = true
	p.mu.Unlock()
	go func() {
		for s.root.Err() == nil {
			_ = s.opts.Proxy.Follow(s.root, func(st proxy.Status) {
				p.mu.Lock()
				p.caPath = st.CAPath
				p.mu.Unlock()
				p.set(func(w *wire.ProxyState) {
					w.Running, w.Error, w.CATrusted, w.Sessions = true, "", st.CATrusted, wireProxy(st)
				})
			})
			if p.snapshot().Running {
				p.set(func(w *wire.ProxyState) { *w = wire.ProxyState{Sessions: []wire.ProxySession{}} })
			}
			select {
			case <-s.root.Done():
				return
			case <-time.After(proxyRecheck):
			}
		}
	}()
}

// startProxy starts the machine's proxy in the background, after its
// one-time network setup (the administrator prompt), and returns once it
// serves.
func (s *Server) startProxy(ctx context.Context) error {
	if _, err := s.backend(); err != nil {
		return err // a configured pier before anything runs as root
	}
	if err := s.opts.Proxy.Start(ctx); err != nil {
		if !errors.Is(err, pier.ErrAdminCancelled) {
			s.proxy.set(func(w *wire.ProxyState) { w.Error = err.Error() })
		}
		return err
	}
	s.followProxy()
	deadline := time.After(10 * time.Second)
	for !s.proxy.snapshot().Running {
		select {
		case <-ctx.Done():
			return nil // it carries on; proxy.watch reports it
		case <-deadline:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// forwards are the ports forwarded by hand, per session.
type forwards struct {
	mu   sync.Mutex
	list []wire.PortForward
}

func init() {
	handle("proxy.start", "Serves every running session as <session>.pier with its ports mirrored on this Mac, from a background proxy shared with `pier proxy` that outlives the app. The first time, macOS asks for an administrator's password (local names and addresses, put back at every boot).",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.ProxyState, error) {
			if err := s.startProxy(ctx); err != nil {
				if errors.Is(err, pier.ErrAdminCancelled) {
					return s.proxy.snapshot(), rpcErr(codeFailed, wire.ErrCancelled, err.Error())
				}
				return s.proxy.snapshot(), err
			}
			return s.proxy.snapshot(), nil
		})
	handle("proxy.stop", "Stops the proxy, however it was started; sessions' names and mirrored ports go away.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (*wire.Empty, error) {
			err := s.opts.Proxy.Stop()
			if errors.Is(err, proxy.ErrNotRunning) {
				err = nil
			}
			return nil, err
		})
	handle("proxy.forget", "Stops the proxy and takes its network setup off this Mac (the .pier resolver, the boot task, the loopback addresses); macOS asks for an administrator's password.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (*wire.Empty, error) {
			err := s.opts.Proxy.Forget()
			if errors.Is(err, pier.ErrAdminCancelled) {
				return nil, rpcErr(codeFailed, wire.ErrCancelled, err.Error())
			}
			return nil, err
		})
	watch("proxy", "The proxy's state, whoever started it: running, each session's name and mirrored ports.",
		func(ctx context.Context, s *Server, _ wire.Empty, sub *Sub[wire.ProxyState]) (wire.ProxyState, error) {
			s.followProxy()
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

	handle("cli.status", "Whether the pier command in the shell is this app's pier: installed, provided another way (Homebrew), or a link left behind by a moved app.",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.CLIStatus, error) {
			self, err := selfPath()
			if err != nil {
				return wire.CLIStatus{}, err
			}
			return wireCLI(pier.CLIInstallStatus(self)), nil
		})
	handle("cli.install", "Installs (or repairs) the pier command, a link to this app's pier; macOS asks for an administrator's password if needed — unless quiet, which then leaves it as it is.",
		func(ctx context.Context, s *Server, _ *Call, p wire.CLIInstallParams) (wire.CLIStatus, error) {
			self, err := selfPath()
			if err != nil {
				return wire.CLIStatus{}, err
			}
			err = pier.InstallCLI(self, !p.Quiet)
			switch {
			case errors.Is(err, pier.ErrNeedsAdmin):
			case errors.Is(err, pier.ErrAdminCancelled):
				return wire.CLIStatus{}, rpcErr(codeFailed, wire.ErrCancelled, err.Error())
			case err != nil:
				return wire.CLIStatus{}, err
			}
			return wireCLI(pier.CLIInstallStatus(self)), nil
		})
}

func wireCLI(st pier.CLIStatus) wire.CLIStatus {
	return wire.CLIStatus{Link: st.Link, Installed: st.Installed, Provided: st.Provided, Stale: st.Stale, Other: st.Other}
}

// selfPath is this pier binary, links resolved.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

package api

import (
	"context"
	"sync"
	"testing"

	"github.com/usepier/pier/internal/proxy"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// fakeProxy is a machine's proxy that serves one session once started.
type fakeProxy struct {
	mu      sync.Mutex
	running bool
	stopped chan struct{}
}

func (p *fakeProxy) Start(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		p.running, p.stopped = true, make(chan struct{})
	}
	return nil
}

func (p *fakeProxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return proxy.ErrNotRunning
	}
	p.running = false
	close(p.stopped)
	return nil
}

func (p *fakeProxy) Forget() error {
	_ = p.Stop()
	return nil
}

func (p *fakeProxy) Follow(ctx context.Context, fn func(proxy.Status)) error {
	p.mu.Lock()
	running, stopped := p.running, p.stopped
	p.mu.Unlock()
	if !running {
		return proxy.ErrNotRunning
	}
	fn(proxy.Status{Sessions: []proxy.SessionStatus{{ID: "i-1", Name: "fix", Ports: []int{3000}}}})
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestProxy(t *testing.T) {
	c := start(t, &fake{sessions: []pier.Session{{ID: "i-1", Name: "fix", State: pier.StateRunning}}})
	resp, _ := c.call("proxy.start", map[string]any{})
	var st wire.ProxyState
	remarshal(t, resp["result"], &st)
	if !st.Running || len(st.Sessions) != 1 || st.Sessions[0].Host != "fix.pier" || st.Sessions[0].Ports[0] != 3000 {
		t.Fatalf("proxy = %+v", resp)
	}
	c.call("ports.forward", map[string]any{"sessionId": "i-1", "local": 8080, "remote": 3000})
	resp, _ = c.call("ports.list", map[string]any{})
	if l, _ := resp["result"].([]any); len(l) != 1 {
		t.Errorf("forwards = %v", resp)
	}
	// A watch sees it stop, however it was stopped.
	resp, _ = c.call("proxy.watch", map[string]any{})
	remarshal(t, resp["result"].(map[string]any)["snapshot"], &st)
	if !st.Running {
		t.Errorf("watch = %+v", resp)
	}
	resp, _ = c.call("proxy.stop", map[string]any{})
	if resp["error"] != nil {
		t.Errorf("stop = %v", resp)
	}
	remarshal(t, c.next("sub.event")["params"].(map[string]any)["data"], &st)
	if st.Running {
		t.Errorf("after stop = %+v", st)
	}
	// Stopping what doesn't run is fine.
	if resp, _ = c.call("proxy.stop", map[string]any{}); resp["error"] != nil {
		t.Errorf("stop again = %v", resp)
	}
}

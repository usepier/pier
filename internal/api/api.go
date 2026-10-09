// Package api serves pier over JSON-RPC 2.0 on stdin/stdout: `pier api`,
// the way the Mac app (and anything else that wants pier as a library over
// a pipe) drives it. It is a child process of its client, the language
// server model: no daemon, no port, and it exits when its stdin closes.
//
// Framing is one JSON message per line (NDJSON). Requests run concurrently;
// `$/cancelRequest` cancels one. Long requests report progress with
// `$/progress` notifications tagged by request id. Live data is a
// subscription: `<topic>.watch` returns {sub, seq, snapshot} and pushes
// `sub.event` notifications until `sub.cancel`. Errors carry
// data.kind (wire.ErrorKind) so clients never parse messages.
//
// Every shape on the wire lives in pkg/pier/wire; internal/api/tsgen turns
// the method table below into the Mac app's TypeScript.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// MaxLine bounds one incoming message. Outgoing data is chunked well below
// it (terminal output, file reads and transcripts are paged).
const MaxLine = 4 << 20

// Options configures a server.
type Options struct {
	In  io.Reader
	Out io.Writer
	// Open builds the backend: at start, and again after a settings change
	// that moves pier to another cloud or account.
	Open    func() (Backend, error)
	Version string // pier's version, reported by initialize
	// Exit ends the process after a shutdown request has been answered.
	Exit func()
	// Log receives diagnostics (stderr in `pier api`). nil discards them.
	Log io.Writer
	// Pier is what setup builds drivers with before any config exists.
	Pier pier.Options
}

// Server is one `pier api` session with one client.
type Server struct {
	opts Options

	wmu sync.Mutex // one writer: lines never interleave
	out *bufio.Writer

	root context.Context // lives as long as the client: subscriptions hang off it

	mu       sync.Mutex
	be       Backend
	beErr    error
	calls    map[string]context.CancelFunc // in-flight requests by id
	subs     map[string]*subscription
	nextSub  int
	activity wire.ClientActivity

	overview *overviewPoller
	termReg  *terminals
	notices  *notices
	prs      *prCache
	proxy    proxyRunner
	pools    poolRefill
	ports    forwards
	client   wire.InitializeParams // what the client said it handles

	chatMu sync.Mutex
	chats  map[string]*chatFollower // followed chats by session id
}

// Serve runs a server until its input closes or ctx ends.
func Serve(ctx context.Context, o Options) error {
	s := &Server{
		opts:  o,
		out:   bufio.NewWriter(o.Out),
		calls: map[string]context.CancelFunc{},
		subs:  map[string]*subscription{},
		// Until the client says otherwise it is looking.
		activity: wire.ClientActivity{Focused: true},
		chats:    map[string]*chatFollower{},
	}
	s.overview = newOverviewPoller(s)
	s.notices = newNotices(s)
	s.prs = &prCache{s: s, prs: map[string]pier.PR{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.root = ctx
	in := bufio.NewScanner(o.In)
	in.Buffer(make([]byte, 64<<10), MaxLine)
	var wg sync.WaitGroup
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var m inMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			s.reply(nil, nil, rpcErr(-32700, wire.ErrParse, "parse error: "+err.Error()))
			continue
		}
		if len(m.ID) == 0 || string(m.ID) == "null" {
			s.notification(ctx, m)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.request(ctx, m)
		}()
	}
	// The client is gone. Work still unwinding (ssh children, cloud calls)
	// gets a moment to see its context cancelled, never longer: a call
	// that ignores cancellation must not keep the process alive.
	cancel()
	s.closeTerminals()
	s.stopProxy()
	settled := make(chan struct{})
	go func() {
		s.overview.stop()
		wg.Wait()
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(exitGrace):
	}
	s.mu.Lock()
	be := s.be
	s.mu.Unlock()
	if be != nil {
		be.Close()
	}
	if err := in.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// exitGrace bounds the wind-down after the client goes away.
const exitGrace = 3 * time.Second

type inMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type outMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// write sends one message as one line.
func (s *Server) write(m outMessage) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		s.logf("encoding %s: %v", m.Method, err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.out.Write(b)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *Server) reply(id json.RawMessage, result any, e *rpcError) {
	if e == nil && result == nil {
		result = json.RawMessage("null") // a success must carry a result
	}
	s.write(outMessage{ID: id, Result: result, Error: e})
}

// notify sends a server→client notification.
func (s *Server) notify(method string, params any) {
	s.write(outMessage{Method: method, Params: params})
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Log != nil {
		fmt.Fprintf(s.opts.Log, "pier api: "+format+"\n", args...)
	}
}

func (s *Server) request(ctx context.Context, m inMessage) {
	meth, ok := methods[m.Method]
	if !ok {
		s.reply(m.ID, nil, rpcErr(-32601, wire.ErrNoMethod, "no method "+m.Method))
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	key := string(m.ID)
	s.mu.Lock()
	s.calls[key] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.calls, key)
		s.mu.Unlock()
		cancel()
	}()
	c := &Call{s: s, id: m.ID}
	result, err := meth.run(ctx, s, c, m.Params)
	if err != nil {
		s.reply(m.ID, nil, toRPCError(err))
		return
	}
	if a, ok := result.(afterReply); ok {
		s.reply(m.ID, a.result, nil)
		a.then()
		return
	}
	s.reply(m.ID, result, nil)
}

// settle waits, at most d, until no request but the caller is in flight.
func (s *Server) settle(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.calls)
		s.mu.Unlock()
		if n <= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// afterReply is a result with work that must follow its reply on the wire:
// a subscription's first event, the exit after shutdown.
type afterReply struct {
	result any
	then   func()
}

func (s *Server) notification(_ context.Context, m inMessage) {
	if h := clientNotes[m.Method]; h != nil {
		h(s, m.Params)
		return
	}
	s.logf("ignoring notification %s", m.Method)
}

// Call is one in-flight request, for handlers that report progress.
type Call struct {
	s  *Server
	id json.RawMessage
}

// Progress sends one progress event for this request.
func (c *Call) Progress(e wire.Event) {
	c.s.notify("$/progress", wire.ProgressParams{ID: c.id, Event: e})
}

// backend returns the current backend, opening it on first use. A failed
// open (no config yet) is retried on the next call, so finishing setup
// mid-session just works.
func (s *Server) backend() (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.be != nil {
		return s.be, nil
	}
	be, err := s.opts.Open()
	if err != nil {
		s.beErr = err
		return nil, err
	}
	s.be, s.beErr = be, nil
	return be, nil
}

// reopen drops the backend so the next call builds one for the current
// config (a cloud or account change).
func (s *Server) reopen() {
	s.mu.Lock()
	old := s.be
	s.be = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s.overview.kick(0)
}

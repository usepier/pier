package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// Terminal output flow control: at most this much output may be in flight
// unacknowledged before the server stops reading. tmux (or the local
// program) absorbs the backlog until the client catches up.
const (
	termChunk  = 32 << 10
	termWindow = 512 << 10
)

// terminal is one open terminal and its flow-control state.
type terminal struct {
	id string
	t  term

	mu      sync.Mutex
	cond    *sync.Cond
	unacked int
	paused  bool
	closed  bool
}

// term is what a terminal needs from pier: *pier.Terminal satisfies it.
type term interface {
	io.ReadWriter
	Resize(cols, rows int) error
	Done() <-chan struct{}
	ExitCode() int
	Close() error
}

type terminals struct {
	mu   sync.Mutex
	next int
	open map[string]*terminal
}

func (s *Server) terms() *terminals {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.termReg == nil {
		s.termReg = &terminals{open: map[string]*terminal{}}
	}
	return s.termReg
}

// startTerminal registers t and pumps its output to the client.
func (s *Server) startTerminal(t term) string {
	reg := s.terms()
	reg.mu.Lock()
	reg.next++
	id := "t" + strconv.Itoa(reg.next)
	tm := &terminal{id: id, t: t}
	tm.cond = sync.NewCond(&tm.mu)
	reg.open[id] = tm
	reg.mu.Unlock()
	go s.pump(tm)
	return id
}

func (s *Server) pump(tm *terminal) {
	buf := make([]byte, termChunk)
	seq := 0
	for {
		tm.mu.Lock()
		for !tm.closed && (tm.paused || tm.unacked >= termWindow) {
			tm.cond.Wait()
		}
		closed := tm.closed
		tm.mu.Unlock()
		if closed {
			break
		}
		n, err := tm.t.Read(buf)
		if n > 0 {
			seq++
			tm.mu.Lock()
			tm.unacked += n
			tm.mu.Unlock()
			s.notify("term.output", wire.TermData{TermID: tm.id, Seq: seq, Data: base64.StdEncoding.EncodeToString(buf[:n])})
		}
		if err != nil {
			break
		}
	}
	<-tm.t.Done()
	s.notify("term.exit", wire.TermExit{TermID: tm.id, Code: tm.t.ExitCode()})
	s.dropTerminal(tm.id)
}

func (s *Server) terminal(id string) *terminal {
	reg := s.terms()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.open[id]
}

func (s *Server) dropTerminal(id string) {
	reg := s.terms()
	reg.mu.Lock()
	tm := reg.open[id]
	delete(reg.open, id)
	reg.mu.Unlock()
	if tm != nil {
		tm.close()
	}
}

func (tm *terminal) close() {
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		return
	}
	tm.closed = true
	tm.cond.Broadcast()
	tm.mu.Unlock()
	tm.t.Close()
}

// closeTerminals hangs up everything when the client goes away.
func (s *Server) closeTerminals() {
	reg := s.terms()
	reg.mu.Lock()
	all := reg.open
	reg.open = map[string]*terminal{}
	reg.mu.Unlock()
	for _, tm := range all {
		tm.close()
	}
}

var errNoTerminal = errors.New("no such terminal")

func init() {
	serverNote[wire.TermData]("term.output", "Terminal output, base64; acknowledge it with term.ack.")
	serverNote[wire.TermExit]("term.exit", "A terminal's process ended.")
	clientNote("term.input", "Keystrokes for a terminal, base64.", func(s *Server, p wire.TermData) {
		if tm := s.terminal(p.TermID); tm != nil {
			if b, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
				tm.t.Write(b)
			}
		}
	})
	clientNote("term.resize", "A terminal's new size.", func(s *Server, p wire.TermSize) {
		if tm := s.terminal(p.TermID); tm != nil {
			tm.t.Resize(p.Cols, p.Rows)
		}
	})
	clientNote("term.ack", "How much terminal output the client has rendered.", func(s *Server, p wire.TermAck) {
		if tm := s.terminal(p.TermID); tm != nil {
			tm.mu.Lock()
			tm.unacked = max(0, tm.unacked-p.Bytes)
			tm.cond.Broadcast()
			tm.mu.Unlock()
		}
	})
	clientNote("term.pause", "Stop sending a hidden terminal's output.", func(s *Server, p wire.TermRef) {
		if tm := s.terminal(p.TermID); tm != nil {
			tm.mu.Lock()
			tm.paused = true
			tm.mu.Unlock()
		}
	})
	clientNote("term.resume", "Send a terminal's output again.", func(s *Server, p wire.TermRef) {
		if tm := s.terminal(p.TermID); tm != nil {
			tm.mu.Lock()
			tm.paused = false
			tm.cond.Broadcast()
			tm.mu.Unlock()
		}
	})

	handle("term.open", "Opens a terminal tab into a session's tmux, on one of its windows. It counts as attached while open.",
		func(ctx context.Context, s *Server, _ *Call, p wire.TermOpenParams) (wire.TermOpened, error) {
			be, err := s.backend()
			if err != nil {
				return wire.TermOpened{}, err
			}
			sess, err := s.overview.session(ctx, p.SessionID)
			if err != nil {
				return wire.TermOpened{}, err
			}
			reg := s.terms()
			reg.mu.Lock()
			tab := fmt.Sprintf("%x%d", os.Getpid(), reg.next+1)
			reg.mu.Unlock()
			t, err := be.OpenTerminal(ctx, sess, tab, p.Window, p.Cols, p.Rows)
			if err != nil {
				return wire.TermOpened{}, err
			}
			return wire.TermOpened{TermID: s.startTerminal(t)}, nil
		})
	handle("term.openLocal", "Opens a terminal on this machine for one of pier's interactive steps (a cloud login, setup).",
		func(ctx context.Context, s *Server, _ *Call, p wire.TermLocalParams) (wire.TermOpened, error) {
			var argv []string
			switch p.Purpose {
			case wire.LocalLogin:
				be, err := s.backend()
				if err != nil {
					return wire.TermOpened{}, err
				}
				cmd := be.ReauthCommand(s.overview.lastError())
				if cmd == nil {
					return wire.TermOpened{}, invalidParams("no expired login to fix")
				}
				argv = cmd.Args
			case wire.LocalSetup:
				exe, err := os.Executable()
				if err != nil {
					return wire.TermOpened{}, err
				}
				argv = []string{exe, "setup"}
			default:
				return wire.TermOpened{}, invalidParams("unknown purpose " + string(p.Purpose))
			}
			t, err := pier.LocalTerminal(argv, p.Cols, p.Rows)
			if err != nil {
				return wire.TermOpened{}, err
			}
			return wire.TermOpened{TermID: s.startTerminal(t)}, nil
		})
	handle("term.close", "Hangs a terminal up. Its tmux windows keep running in the session.",
		func(ctx context.Context, s *Server, _ *Call, p wire.TermRef) (*wire.Empty, error) {
			if s.terminal(p.TermID) == nil {
				return nil, errNoTerminal
			}
			s.dropTerminal(p.TermID)
			return nil, nil
		})

	handle("tmux.windows", "A session's tmux windows, in order.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SessionRef) ([]wire.Window, error) {
			be, sess, err := s.sessionFor(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			ws, err := be.Windows(ctx, sess)
			return wireWindows(ws), err
		})
	handle("tmux.newWindow", "Adds a window in the session's checkout and returns it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.NewWindowParams) (wire.Window, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return wire.Window{}, err
			}
			w, err := be.NewWindow(ctx, sess, p.Name)
			return wireWindow(w), err
		})
	handle("tmux.killWindow", "Closes a window and whatever runs in it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.WindowParams) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return nil, err
			}
			return nil, be.KillWindow(ctx, sess, p.Window)
		})
	handle("tmux.renameWindow", "Renames a window.",
		func(ctx context.Context, s *Server, _ *Call, p wire.RenameWindowParams) (*wire.Empty, error) {
			be, sess, err := s.sessionFor(ctx, p.SessionID)
			if err != nil {
				return nil, err
			}
			return nil, be.RenameWindow(ctx, sess, p.Window, p.Name)
		})
}

func (s *Server) sessionFor(ctx context.Context, id string) (Backend, pier.Session, error) {
	be, err := s.backend()
	if err != nil {
		return nil, pier.Session{}, err
	}
	sess, err := s.overview.session(ctx, id)
	return be, sess, err
}

func wireWindow(w pier.Window) wire.Window {
	return wire.Window{ID: w.ID, Index: w.Index, Name: w.Name, Command: w.Command, Active: w.Active, Role: w.Role}
}

func wireWindows(ws []pier.Window) []wire.Window {
	out := make([]wire.Window, 0, len(ws))
	for _, w := range ws {
		out = append(out, wireWindow(w))
	}
	return out
}

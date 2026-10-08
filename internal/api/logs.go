package api

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/usepier/pier/pkg/pier/wire"
)

func init() {
	handle("logs.setup", "The tail of a session's setup log (a failed create's own log for a create that never finished).",
		func(ctx context.Context, s *Server, _ *Call, p wire.LogParams) (wire.LogText, error) {
			be, err := s.backend()
			if err != nil {
				return wire.LogText{}, err
			}
			sess, err := s.overview.session(ctx, p.ID)
			if err != nil {
				return wire.LogText{}, err
			}
			lines := p.Lines
			if lines <= 0 {
				lines = 2000
			}
			text, err := be.SetupLog(ctx, sess, lines)
			if err != nil {
				return wire.LogText{}, err
			}
			out := wire.LogText{Text: text}
			if strings.HasPrefix(text, "… older lines trimmed") {
				_, rest, _ := strings.Cut(text, "\n")
				out.Text, out.Truncated = rest, true
			}
			return out, nil
		})

	handle("auth.login", "Runs the cloud login that fixes the last expired-login error, narrating its output (browser prompts, device codes) as progress.",
		func(ctx context.Context, s *Server, c *Call, _ wire.Empty) (*wire.Empty, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			cmd := be.ReauthCommand(s.overview.lastError())
			if cmd == nil {
				return nil, nil // nothing expired
			}
			if err := runLogin(ctx, c, cmd); err != nil {
				return nil, err
			}
			s.reopen() // fresh credentials: start from a clean client
			s.overview.kick(0)
			return nil, nil
		})
}

// runLogin runs a cloud login with no terminal, narrating its output
// (browser prompts, device codes) as the call's progress.
func runLogin(ctx context.Context, c *Call, cmd *exec.Cmd) error {
	cmd.Stdin = nil
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				c.Progress(wire.Event{Kind: wire.EventNote, Message: line})
			}
		}
	}()
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()
	err := cmd.Wait()
	w.Close()
	wg.Wait()
	return err
}

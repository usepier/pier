package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/usepier/pier/internal/api"
	"github.com/usepier/pier/pkg/pier"
)

// cmdAPI serves pier over JSON-RPC on stdin/stdout for the Mac app (and
// anything else driving pier through a pipe). The protocol owns stdout: the
// real descriptor moves to a private one and fd 1 becomes stderr, so a stray
// print anywhere — in pier or in a child process — lands in the log instead
// of corrupting the stream.
func cmdAPI(args []string) {
	// --client names the caller in the log; nothing else is accepted.
	for _, a := range args {
		if !strings.HasPrefix(a, "--client=") {
			fatal(fmt.Errorf("usage: pier api [--client=<name/version>]"))
		}
		fmt.Fprintln(os.Stderr, "pier api: serving "+strings.TrimPrefix(a, "--client="))
	}
	proto, err := takeStdout()
	if err != nil {
		fatal(err)
	}
	log.SetOutput(os.Stderr)
	api.AdoptLoginEnv()

	opts := options()
	opts.Out = nil // meters and bake output have no terminal to go to
	opts.Mux = true
	opts.Notify = func(msg string) { fmt.Fprintln(os.Stderr, "pier api: "+msg) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exit := func() {
		proto.Close()
		os.Exit(0)
	}
	err = api.Serve(ctx, api.Options{
		In:  os.Stdin,
		Out: proto,
		Open: func() (api.Backend, error) {
			c, err := pier.Open(opts)
			if err != nil {
				return nil, err // never a typed-nil *Client inside the interface
			}
			return c, nil
		},
		Version: version,
		Log:     os.Stderr,
		Exit:    exit,
		Pier:    opts,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pier api:", err)
		os.Exit(1)
	}
	exit()
}

// takeStdout moves the process's stdout to a new descriptor for the
// protocol and points fd 1 (and os.Stdout) at stderr.
func takeStdout() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := dup2(int(os.Stderr.Fd()), 1); err != nil {
		return nil, err
	}
	os.Stdout = os.Stderr
	return os.NewFile(uintptr(fd), "pier-api"), nil
}

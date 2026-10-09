package main

// Reaching sessions' ports from this machine: the proxy and forwards.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/proxy"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

// cmdProxy: `pier proxy` — every running session gets a hostname
// (<session>.pier) with its listening ports mirrored automatically; live
// connections keep the session from parking. One proxy runs per machine:
// in the foreground until ctrl-c, or in the background (start, stop,
// status), shared with the app. forget takes its network setup away.
func cmdProxy(args []string) {
	// net/http logs transport chatter ("Unsolicited response received...")
	// through the global logger when probed ports answer oddly. All of the
	// proxy's real output goes through Options.Out, so drop the rest.
	log.SetOutput(io.Discard)
	dir := config.Dir()
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		if proxy.Running(dir) {
			fmt.Println(ui.Dim.Render("the proxy already runs in the background — `pier proxy status` shows it, `pier proxy stop` stops it"))
			return
		}
		c := open()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := proxy.Serve(ctx, c.Driver(), proxy.Options{StateDir: dir, Out: os.Stdout}); err != nil {
			fatal(err)
		}
	case "start":
		if proxy.Running(dir) {
			fmt.Println(ui.Dim.Render("the proxy already runs"))
			return
		}
		open() // a configured pier, before anything runs as root
		if err := proxy.EnsureNet(os.Stdout, nil); err != nil {
			fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			fatal(err)
		}
		if err := proxy.StartBackground(dir, []string{exe, "proxy", "serve"}); err != nil {
			fatal(err)
		}
		fmt.Println(ui.OK.Render("the proxy runs in the background") + ui.Dim.Render(" — sessions resolve as <session>.pier"))
	case "serve": // hidden: the background proxy itself
		c := open()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := proxy.Serve(ctx, c.Driver(), proxy.Options{StateDir: dir, Out: os.Stdout,
			Privileged: func(string) error {
				return errors.New("the network setup is gone — start the proxy again with `pier proxy start` or from the app")
			}})
		if err != nil && !errors.Is(err, proxy.ErrRunning) {
			fatal(err)
		}
	case "stop":
		if err := proxy.Stop(dir); err != nil {
			fatal(err)
		}
		fmt.Println(ui.Dim.Render("the proxy stopped"))
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := proxy.Follow(ctx, dir, func(st proxy.Status) {
			fmt.Println(ui.OK.Render("the proxy runs"))
			for _, s := range st.Sessions {
				fmt.Printf("  %s %s\n", ui.Bold.Render(s.Name+".pier"), ui.Dim.Render(fmt.Sprint(s.Ports)))
			}
			cancel()
		})
		if errors.Is(err, proxy.ErrNotRunning) {
			fmt.Println(ui.Dim.Render("the proxy isn't running — `pier proxy start` starts it in the background"))
		}
	case "forget":
		if proxy.Running(dir) {
			if err := proxy.Stop(dir); err != nil {
				fatal(err)
			}
		}
		if err := proxy.ForgetNet(nil); err != nil {
			fatal(err)
		}
		fmt.Println(ui.Dim.Render("the proxy's network setup is gone (resolver, boot task, loopback addresses)"))
	default:
		fatal(fmt.Errorf("usage: pier proxy [start | stop | status | forget]"))
	}
}

// cmdPort: `pier port <session> <port> [port...]` — hold ssh -L forwards open
// by hand. The zero-sudo, works-anywhere fallback to `pier proxy`.
func cmdPort(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: pier port <session> <port> [port...]   (3000, or local:session like 8080:3000)"))
	}
	c := open()
	s := match(c, args[0])
	var pairs [][2]int
	for _, a := range args[1:] {
		p, err := pier.ParsePortPair(a)
		if err != nil {
			fatal(err)
		}
		pairs = append(pairs, p)
	}
	resumeIfParked(c, s)
	for _, p := range pairs {
		fmt.Printf("  %s -> %s\n", ui.Bold.Render(fmt.Sprintf("localhost:%d", p[0])),
			ui.Dim.Render(fmt.Sprintf("%s:%d", s.Name, p[1])))
	}
	fmt.Println(ui.Dim.Render("  forwarding — ctrl-c to stop"))
	cmd, err := c.PortForwardCommand(context.Background(), s, pairs)
	if err != nil {
		fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("port forward: %w", err))
	}
}

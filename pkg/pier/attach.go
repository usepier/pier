package pier

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/sshmux"
)

// While someone is attached, the session behaves as if it ran on their
// laptop for the two things a headless VM can't do itself: a URL opened in
// the session (an agent's OAuth login, a CLI's web login, "open in browser")
// opens in the laptop's browser, and a port the session starts listening on
// (an OAuth callback, a dev server) is reachable at the same port on the
// laptop. Together they make any in-session browser login work — for every
// agent and CLI, since they hook the standard opener and plain ports.

// remoteOpenSocket is where the session's opener (xdg-open, $BROWSER →
// pier-supervisor open) looks for the attached laptop.
const remoteOpenSocket = "/home/agent/.pier/open.sock"

// mirrorPoll is how often the attach companion re-reads the session's ports.
var mirrorPoll = 2 * time.Second

// Attach prepares one attach: the ssh+tmux command to run in the foreground,
// with the companion that runs alongside it. stop ends the companion; call
// it when the command exits.
func (c *Client) Attach(ctx context.Context, s Session) (cmd *exec.Cmd, stop func(), err error) {
	cmd, err = c.drv.AttachCommand(ctx, s.ID)
	if err != nil {
		return nil, nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	stops := []func(){}

	// The opener: a local socket the attach connection forwards into the
	// session. A failure to listen leaves the attach working, just without
	// browser opens.
	if sock, closeOpener, err := listenOpener(); err == nil {
		stops = append(stops, closeOpener)
		cmd.Args = slices.Insert(cmd.Args, 1, "-R", remoteOpenSocket+":"+sock)
	}

	// The port mirror rides a multiplexed connection, so forwards can come
	// and go while the attach runs: the client's own when it keeps them,
	// else one that lives exactly as long as this attach.
	mux := c.mux
	if mux == nil {
		mux = sshmux.New(filepath.Join(config.Dir(), "mux"), c.drv.SSHTarget)
		mux.CleanStale()
		stops = append(stops, mux.Close)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if conn, err := mux.Conn(cctx, s.ID); err == nil {
			mirrorPorts(cctx, conn)
		}
	}()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			wg.Wait()
			for _, f := range stops {
				f()
			}
		})
	}
	return cmd, stop, nil
}

// listenOpener serves URL opens from the session: one http(s) URL per
// connection, opened with the laptop's own opener.
func listenOpener() (sock string, closeFn func(), err error) {
	sock = filepath.Join(os.TempDir(), "pier-open-"+randHex(6)+".sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return "", nil, err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serveOpen(conn)
		}
	}()
	return sock, func() { l.Close(); os.Remove(sock) }, nil
}

func serveOpen(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	if err := openURL(strings.TrimSpace(line)); err != nil {
		fmt.Fprintln(conn, err.Error())
		return
	}
	fmt.Fprintln(conn, "ok")
}

// openURL opens an http(s) URL in this machine's browser. Nothing else is
// accepted: the session asks for a web page, not for a program to run.
func openURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("not an http(s) URL")
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return openCommand(opener, u.String()).Start()
}

// openCommand builds the opener process; a var so tests don't open a
// browser.
var openCommand = exec.Command

// mirrorPorts forwards every port the session listens on to the same port
// on this machine until ctx ends, then takes the forwards down. Ports
// already taken here (another session's, a local server's) are left alone;
// privileged ports are skipped.
func mirrorPorts(ctx context.Context, conn *sshmux.Conn) {
	forwarded := map[int]bool{}
	var live []string
	defer func() {
		for _, spec := range live {
			_ = conn.Cancel("-L", spec)
		}
	}()
	for {
		out, err := conn.Exec(ctx, "cat /run/pier/status.json", nil)
		if err == nil {
			var st struct {
				Listening []int `json:"listening"`
			}
			if json.Unmarshal(out, &st) == nil {
				for _, p := range st.Listening {
					if p < 1024 || forwarded[p] {
						continue
					}
					forwarded[p] = true // tried once: a taken port stays skipped
					spec := fmt.Sprintf("127.0.0.1:%d:localhost:%d", p, p)
					if conn.Forward("-L", spec) == nil {
						live = append(live, spec)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(mirrorPoll):
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

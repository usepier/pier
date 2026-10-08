package pier

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/usepier/pier/internal/driver/payload"
)

// A session's supervisor streams what happens in it — status changes, what
// runs there — and takes changes to make, over one ssh channel on the
// session's connection. Following never counts as attached.

// streamProto is the supervisor stream protocol this pier speaks; a
// supervisor reporting an older one is replaced.
const streamProto = 1

// errNoStream: the session's supervisor predates the stream.
var errNoStream = errors.New("no stream socket")

// Follow streams a session's topics ("status", "env") to fn, one message
// at a time (its event name and JSON data), until ctx ends or the
// connection drops. An older supervisor is upgraded in place first.
func (c *Client) Follow(ctx context.Context, s Session, topics []string, fn func(ev string, data json.RawMessage)) error {
	err := c.follow(ctx, s, topics, fn)
	if errors.Is(err, errNoStream) {
		if uerr := c.UpgradeSupervisor(ctx, s); uerr != nil {
			return fmt.Errorf("upgrading the session's supervisor: %w", uerr)
		}
		err = c.follow(ctx, s, topics, fn)
	}
	return err
}

func (c *Client) follow(ctx context.Context, s Session, topics []string, fn func(string, json.RawMessage)) error {
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return err
	}
	cmd := conn.Command(ctx, nil, "exec pier-supervisor stream")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	if !sc.Scan() {
		cmd.Wait()
		if strings.Contains(stderr.String(), "no stream socket") || strings.Contains(stderr.String(), "not found") {
			return errNoStream
		}
		return fmt.Errorf("session stream: %s", strings.TrimSpace(stderr.String()))
	}
	var hello struct {
		Ev   string `json:"ev"`
		Data struct {
			Proto int `json:"proto"`
		} `json:"data"`
	}
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.Ev != "hello" || hello.Data.Proto < streamProto {
		return errNoStream
	}
	sub, _ := json.Marshal(map[string]any{"op": "subscribe", "topics": topics})
	if _, err := stdin.Write(append(sub, '\n')); err != nil {
		return err
	}
	conn.Busy()
	for sc.Scan() {
		var m struct {
			Ev   string          `json:"ev"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Ev != "" {
			fn(m.Ev, m.Data)
			conn.Busy()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("session stream ended")
}

// UpgradeSupervisor replaces a session's supervisor with this pier's and
// restarts it — the same install a create or claim does. The restart
// resets its idle clock, which only delays a park.
func (c *Client) UpgradeSupervisor(ctx context.Context, s Session) error {
	bin, err := c.drv.Supervisor(ctx, s.InstanceType)
	if err != nil {
		return err
	}
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "cat > /tmp/pier-supervisor", bytes.NewReader(bin)); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, payload.InstallSupervisorScript(), nil)
	if c.opts.Notify != nil && err == nil {
		c.opts.Notify("updated " + s.Name + "'s supervisor to this pier's")
	}
	return err
}

// Op asks a session's supervisor to make a change and returns its answer.
// The change runs in the supervisor, so a dropped connection can't leave it
// half done; the op id makes a retry of the same request a no-op.
func (c *Client) Op(ctx context.Context, s Session, op string, args any) (json.RawMessage, error) {
	if err := CheckReady(s); err != nil {
		return nil, err
	}
	req, err := json.Marshal(map[string]any{"id": newJobID(), "op": op, "args": args})
	if err != nil {
		return nil, err
	}
	run := func() (string, error) {
		return c.exec(ctx, s.ID, "pier-supervisor op "+shellQuote(string(req)))
	}
	out, err := run()
	if err != nil && strings.Contains(err.Error(), "no stream socket") {
		if uerr := c.UpgradeSupervisor(ctx, s); uerr != nil {
			return nil, uerr
		}
		out, err = run()
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// Env is what runs in a session, as the environment view shows it.
type Env struct {
	Windows    []Window
	Containers []Container
	Processes  []Process
	Ports      []Port
	Res        *Resources
	At         time.Time
}

// Container is one docker container in a session.
type Container struct {
	ID, Name, Image string
	State           string // running, exited, …
	Status          string // docker's own words: "Up 2 hours"
	Ports           string
}

// Process is one of the heaviest processes in a session.
type Process struct {
	PID     int
	Command string
	CPU     float64 // % of one CPU
	RSS     uint64  // bytes
}

// ParseEnv reads an env message from the stream.
func ParseEnv(data json.RawMessage) (Env, error) {
	var raw struct {
		Windows    string    `json:"windows"`
		Containers string    `json:"containers"`
		Processes  []Process `json:"processes"`
		Ports      []struct {
			Port    int    `json:"port"`
			Process string `json:"process"`
			PID     int    `json:"pid"`
		} `json:"ports"`
		Res *struct {
			CPU       float64 `json:"cpu"`
			CPUs      int     `json:"cpus"`
			MemUsed   uint64  `json:"mem_used"`
			MemTotal  uint64  `json:"mem_total"`
			DiskUsed  uint64  `json:"disk_used"`
			DiskTotal uint64  `json:"disk_total"`
			Load1     float64 `json:"load1"`
		} `json:"res"`
		At time.Time `json:"at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Env{}, err
	}
	e := Env{Windows: parseWindows(raw.Windows), Processes: raw.Processes, At: raw.At}
	for _, p := range raw.Ports {
		e.Ports = append(e.Ports, Port{Port: p.Port, Process: p.Process, PID: p.PID})
	}
	if raw.Res != nil {
		r := Resources(*raw.Res)
		e.Res = &r
	}
	for _, line := range strings.Split(strings.TrimSpace(raw.Containers), "\n") {
		var d struct{ ID, Names, Image, State, Status, Ports string }
		if json.Unmarshal([]byte(line), &d) == nil && d.ID != "" {
			e.Containers = append(e.Containers, Container{ID: d.ID, Name: d.Names, Image: d.Image, State: d.State, Status: d.Status, Ports: d.Ports})
		}
	}
	return e, nil
}

// ContainerAction starts, stops or restarts a container in a session.
func (c *Client) ContainerAction(ctx context.Context, s Session, container, action string) error {
	_, err := c.Op(ctx, s, "container", map[string]string{"action": action, "container": container})
	return err
}

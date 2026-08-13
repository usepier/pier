package piercore

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"golang.org/x/crypto/ssh"
)

// tmux serializes control characters in format output as their octal escape,
// so the wire delimiter is the literal four-character sequence `\037`.
const tmuxSeparator = `\037`

type remoteClient struct {
	client *ssh.Client
	mu     sync.Mutex
}

func (r *remoteClient) close() error { return r.client.Close() }

func (c *Client) remote(ctx context.Context, id string) (remoteInstance, error) {
	c.mu.Lock()
	remote, ok := c.instances[id]
	c.mu.Unlock()
	if ok {
		return remote, nil
	}
	items, err := c.loadInstances(ctx)
	if err != nil {
		return remoteInstance{}, err
	}
	for _, item := range items {
		if item.Model.ID == id {
			return item, nil
		}
	}
	return remoteInstance{}, fmt.Errorf("Pier instance %q was not found", id)
}

func (c *Client) sshClient(ctx context.Context, id string) (*remoteClient, error) {
	c.mu.Lock()
	if client := c.remotes[id]; client != nil {
		c.mu.Unlock()
		return client, nil
	}
	c.mu.Unlock()

	remote, err := c.remote(ctx, id)
	if err != nil {
		return nil, err
	}
	if remote.Host == "" || remote.AvailabilityZone == "" {
		return nil, errors.New("the Pier instance has no reachable public address yet")
	}
	session, err := c.configuredSession(ctx)
	if err != nil {
		return nil, err
	}
	config, err := authenticatedConfig(ctx, session)
	if err != nil {
		return nil, err
	}
	if remote.SecurityGroupID != "" {
		if err := authorizeCurrentAddress(ctx, config, remote.SecurityGroupID); err != nil {
			return nil, err
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("create temporary SSH key: %w", err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("encode temporary SSH key: %w", err)
	}
	output, err := ec2instanceconnect.NewFromConfig(config).SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId: aws.String(id), InstanceOSUser: aws.String("agent"),
		AvailabilityZone: aws.String(remote.AvailabilityZone),
		SSHPublicKey:     aws.String(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublicKey)))),
	})
	if err != nil {
		return nil, fmt.Errorf("authorize temporary SSH key: %w", err)
	}
	if !output.Success {
		return nil, errors.New("EC2 Instance Connect rejected the temporary SSH key")
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("prepare temporary SSH key: %w", err)
	}
	sshConfig := &ssh.ClientConfig{
		User: "agent", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Instance keys rotate with disposable Pier VMs.
		Timeout:         15 * time.Second,
	}
	client, err := dialSSH(
		ctx,
		net.JoinHostPort(remote.Host, "22"),
		sshConfig,
		15*time.Second,
	)
	if err != nil {
		return nil, fmt.Errorf("connect to Pier instance: %w", err)
	}
	result := &remoteClient{client: client}
	c.mu.Lock()
	if existing := c.remotes[id]; existing != nil {
		c.mu.Unlock()
		_ = result.close()
		return existing, nil
	}
	c.remotes[id] = result
	c.mu.Unlock()
	return result, nil
}

func dialSSH(
	ctx context.Context,
	address string,
	config *ssh.ClientConfig,
	timeout time.Duration,
) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = connection.Close()
		return nil, err
	}

	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(clientConnection, channels, requests), nil
}

func authorizeCurrentAddress(ctx context.Context, config aws.Config, groupID string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://checkip.amazonaws.com", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("discover this device's public address: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return fmt.Errorf("read this device's public address: %w", err)
	}
	address := strings.TrimSpace(string(data))
	ip := net.ParseIP(address)
	if ip == nil {
		return errors.New("the public address service returned an invalid address")
	}
	permission := ec2types.IpPermission{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
	}
	if ip.To4() != nil {
		permission.IpRanges = []ec2types.IpRange{{
			CidrIp: aws.String(address + "/32"), Description: aws.String("pier-ios"),
		}}
	} else {
		permission.Ipv6Ranges = []ec2types.Ipv6Range{{
			CidrIpv6: aws.String(address + "/128"), Description: aws.String("pier-ios"),
		}}
	}
	_, err = ec2.NewFromConfig(config).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(groupID), IpPermissions: []ec2types.IpPermission{permission},
	})
	if apiErrorCode(err) == "InvalidPermission.Duplicate" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("allow SSH from this device: %w", err)
	}
	return nil
}

func (c *Client) runRemote(ctx context.Context, id, command string) (string, error) {
	client, err := c.sshClient(ctx, id)
	if err != nil {
		return "", err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	session, err := client.client.NewSession()
	if err != nil {
		c.dropRemote(id, client)
		return "", fmt.Errorf("open Pier SSH session: %w", err)
	}
	defer session.Close()
	output, err := session.CombinedOutput("bash -lc " + shellQuote(command))
	if err != nil {
		return "", fmt.Errorf("run Pier command: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

func (c *Client) dropRemote(id string, client *remoteClient) {
	c.mu.Lock()
	if c.remotes[id] == client {
		delete(c.remotes, id)
	}
	c.mu.Unlock()
	_ = client.close()
}

func inspectionCommand() string {
	format := strings.Join([]string{
		"#{window_id}", "#{window_name}", "#{window_active}", "#{window_panes}",
		"#{pane_current_command}", "#{pane_current_path}",
	}, tmuxSeparator)
	return "cat /run/pier/status.json 2>/dev/null || printf '{}'; printf '\n---PIER-APP-TABS---\n'; " +
		"tmux list-windows -t main -F " + shellQuote(format) + " 2>/dev/null || true"
}

func parseInspection(model instance, output string) (snapshot, error) {
	statusRaw, tabsRaw, ok := strings.Cut(output, "---PIER-APP-TABS---")
	if !ok {
		return snapshot{}, errors.New("session returned an incomplete inspection response")
	}
	var status struct {
		State     string `json:"state"`
		Listening []int  `json:"listening"`
		Strained  bool   `json:"strained"`
		Setup     string `json:"setup"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(statusRaw)), &status)
	if status.State == "working" || status.State == "idle" {
		model.State = status.State
	}
	model.Strained = status.Strained
	if status.Setup != "" {
		model.Setup = status.Setup
	}
	tabs := []tab{}
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(tabsRaw)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), tmuxSeparator)
		if len(fields) != 6 {
			continue
		}
		panes, _ := strconv.Atoi(fields[3])
		tabs = append(tabs, tab{
			ID: fields[0], Name: fields[1], Active: fields[2] == "1", Panes: panes,
			Command: fields[4], WorkingDirectory: fields[5],
		})
	}
	ports := make([]port, 0, len(status.Listening))
	for _, number := range status.Listening {
		ports = append(ports, port{Number: number, IsHTTP: likelyHTTP(number)})
	}
	return snapshot{Instance: model, Tabs: tabs, Ports: ports}, nil
}

func likelyHTTP(number int) bool {
	switch number {
	case 80, 443, 3000, 3001, 4173, 5000, 5173, 8000, 8080, 8081, 8888:
		return true
	default:
		return false
	}
}

// TerminalListener is implemented by Swift and receives raw PTY output. The
// listener may be called from a Go background goroutine.
type TerminalListener interface {
	Output(data []byte)
	Closed(message string)
}

// Terminal is a live SSH PTY attached to a cloned tmux client session.
type Terminal struct {
	client   *remoteClient
	session  *ssh.Session
	stdin    io.WriteCloser
	listener TerminalListener
	mu       sync.Mutex
	closed   bool
}

func (c *Client) OpenTerminal(instanceID, tabID string, listener TerminalListener) (*Terminal, error) {
	if !validTabID.MatchString(tabID) {
		return nil, fmt.Errorf("invalid tmux window id %q", tabID)
	}
	client, err := c.sshClient(context.Background(), instanceID)
	if err != nil {
		return nil, err
	}
	session, err := client.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open terminal: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("open terminal input: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("open terminal output: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("open terminal error output: %w", err)
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("request terminal: %w", err)
	}
	script := attachScript(tabID)
	if err := session.Start("bash -lc " + shellQuote(script)); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("attach tmux terminal: %w", err)
	}
	terminal := &Terminal{client: client, session: session, stdin: stdin, listener: listener}
	go terminal.copyOutput(stdout)
	go terminal.copyOutput(stderr)
	go terminal.wait()
	return terminal, nil
}

func attachScript(tabID string) string {
	return `set -e
client_session="pier-ios-$$"
cleanup() { tmux kill-session -t "$client_session" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM
tmux set-option -g set-titles on
tmux set-option -g set-titles-string '#{pane_current_command}'
window_index=$(tmux display-message -p -t ` + shellQuote(tabID) + ` '#{window_index}')
tmux new-session -d -t main -s "$client_session"
tmux select-window -t "$client_session:$window_index"
tmux attach-session -t "$client_session"`
}

func (t *Terminal) copyOutput(reader io.Reader) {
	buffer := make([]byte, 32*1024)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			t.listener.Output(data)
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) wait() {
	err := t.session.Wait()
	message := ""
	if err != nil && !errors.Is(err, io.EOF) {
		var exitError *ssh.ExitError
		if !errors.As(err, &exitError) {
			message = err.Error()
		}
	}
	t.mu.Lock()
	alreadyClosed := t.closed
	t.closed = true
	t.mu.Unlock()
	if !alreadyClosed {
		t.listener.Closed(message)
	}
}

func (t *Terminal) Write(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("terminal is closed")
	}
	_, err := t.stdin.Write(data)
	return err
}

func (t *Terminal) Resize(columns, rows int) error {
	if columns < 1 || rows < 1 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	return t.session.WindowChange(rows, columns)
}

func (t *Terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.stdin.Close()
	return t.session.Close()
}

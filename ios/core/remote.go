package piercore

// remote.go: ssh to a session, straight to sshd on its public IP. The
// session's own key lives on the Mac that made it, so the phone sends a
// key of its own through EC2 Instance Connect (good for 60 seconds, enough
// to log in), and opens TCP 22 to its address with a rule of its own in
// pier's security group — never the Mac's, which each would otherwise
// revoke from the other.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"golang.org/x/crypto/ssh"
)

// The user work happens as in every session.
const sessionUser = "agent"

type remoteClient struct {
	client *ssh.Client
	mu     sync.Mutex
}

func (r *remoteClient) close() error { return r.client.Close() }

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
	if remote.Model.State == "parked" {
		return nil, errors.New("the session is parked: wake it first")
	}
	if remote.Host == "" || remote.Zone == "" {
		return nil, errors.New("the session has no public address yet")
	}
	config, me, err := c.aws(ctx)
	if err != nil {
		return nil, err
	}
	if remote.SecurityGroup != "" {
		if err := c.ensureDirectRule(ctx, config, remote.SecurityGroup, me); err != nil {
			return nil, err
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("make a key: %w", err)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, fmt.Errorf("encode the key: %w", err)
	}
	sent, err := ec2instanceconnect.NewFromConfig(config).SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId: aws.String(id), InstanceOSUser: aws.String(sessionUser),
		AvailabilityZone: aws.String(remote.Zone),
		SSHPublicKey:     aws.String(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic)))),
	})
	if err != nil {
		return nil, fmt.Errorf("send the session a key (EC2 Instance Connect): %w", err)
	}
	if !sent.Success {
		return nil, errors.New("EC2 Instance Connect refused the key")
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, fmt.Errorf("prepare the key: %w", err)
	}
	sshConfig := &ssh.ClientConfig{
		User: sessionUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// A session's host key is new with every instance, and the address
		// is the one EC2 just named for it.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	client, err := dialSSH(ctx, net.JoinHostPort(remote.Host, "22"), sshConfig, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to the session: %w", err)
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

func dialSSH(ctx context.Context, address string, config *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(clientConn, channels, requests), nil
}

// directRuleDesc names the Mac's rule ("pier direct <user>"); the phone's
// is the same with " ios" after it.
const directRuleDesc = "pier direct"

// ensureDirectRule keeps the phone's one inbound rule: TCP 22 from its
// current public IPv4, its older addresses revoked. Rules of the Mac and of
// other people are left alone.
func (c *Client) ensureDirectRule(ctx context.Context, config aws.Config, group, me string) error {
	ip, err := publicIPv4(ctx)
	if err != nil {
		return err
	}
	cidr := ip + "/32"
	c.mu.Lock()
	done := c.ensured[group] == cidr
	c.mu.Unlock()
	if done {
		return nil
	}
	desc := directRuleDesc + " " + ruleUser(me) + " ios"
	client := ec2.NewFromConfig(config)
	rules, err := client.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{group}}},
	})
	if err != nil {
		return fmt.Errorf("read pier's security group: %w", err)
	}
	have := false
	for _, r := range rules.SecurityGroupRules {
		if aws.ToBool(r.IsEgress) || aws.ToString(r.Description) != desc {
			continue
		}
		if aws.ToString(r.CidrIpv4) == cidr {
			have = true
			continue
		}
		_, _ = client.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
			GroupId: aws.String(group), SecurityGroupRuleIds: []string{aws.ToString(r.SecurityGroupRuleId)},
		})
	}
	if !have {
		_, err := client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(group),
			IpPermissions: []ec2types.IpPermission{{
				IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
				IpRanges: []ec2types.IpRange{{CidrIp: aws.String(cidr), Description: aws.String(desc)}},
			}},
		})
		if err != nil && apiErrorCode(err) != "InvalidPermission.Duplicate" {
			return fmt.Errorf("let this phone reach the session: %w", err)
		}
	}
	c.mu.Lock()
	c.ensured[group] = cidr
	c.mu.Unlock()
	return nil
}

// ruleUser is the caller's IAM name (the ARN's last segment) in the
// characters rule descriptions accept, as the Mac's pier writes it.
func ruleUser(arn string) string {
	var b strings.Builder
	for _, r := range arn[strings.LastIndex(arn, "/")+1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '@':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// ipServices answer with the caller's public IPv4; the first to answer
// wins (any one may be blocked on a given network).
var ipServices = []string{
	"https://checkip.amazonaws.com",
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
}

func publicIPv4(ctx context.Context) (string, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", addr)
		}},
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	answers := make(chan string, len(ipServices))
	for _, u := range ipServices {
		go func(u string) { answers <- lookupIP(ctx, client, u) }(u)
	}
	for range ipServices {
		if ip := <-answers; ip != "" {
			return ip, nil
		}
	}
	return "", errors.New("couldn't learn this phone's public address")
}

func lookupIP(ctx context.Context, client *http.Client, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
		return ""
	}
	return s
}

// runRemote runs a script in the session (bash -lc) and returns its output.
func (c *Client) runRemote(ctx context.Context, id, script string) (string, error) {
	client, err := c.sshClient(ctx, id)
	if err != nil {
		return "", err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	s, err := client.client.NewSession()
	if err != nil {
		c.dropRemote(id, client)
		return "", fmt.Errorf("talk to the session: %w", err)
	}
	defer s.Close()
	out, err := s.CombinedOutput("bash -lc " + shellQuote(script))
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

func (c *Client) dropRemote(id string, client *remoteClient) {
	c.mu.Lock()
	if c.remotes[id] == client {
		delete(c.remotes, id)
	}
	c.mu.Unlock()
	_ = client.close()
}

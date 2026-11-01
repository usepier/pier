// Package piercore is pier for the phone: IAM Identity Center sign-in, the
// user's sessions in EC2, and ssh into their tmux — what Swift calls through
// gomobile. Everything crosses as JSON strings.
package piercore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// Client owns the sign-in. Swift keeps ExportSession in the Keychain; the
// core never writes credentials anywhere.
type Client struct {
	mu        sync.Mutex
	session   sessionState
	pending   *pendingAuthorization
	sessions  map[string]remoteSession
	remotes   map[string]*remoteClient
	ensured   map[string]string // security group → the cidr its rule has
	config    *aws.Config
	me        string
	configTil time.Time
}

// NewClient restores a client from what ExportSession returned ("" before
// the first sign-in).
func NewClient(sessionJSON string) (*Client, error) {
	c := &Client{}
	c.reset()
	if strings.TrimSpace(sessionJSON) != "" {
		if err := json.Unmarshal([]byte(sessionJSON), &c.session); err != nil {
			return nil, fmt.Errorf("restore the sign-in: %w", err)
		}
	}
	return c, nil
}

func (c *Client) reset() {
	c.sessions = map[string]remoteSession{}
	c.remotes = map[string]*remoteClient{}
	c.ensured = map[string]string{}
	c.config, c.me = nil, ""
}

// ExportSession is the sign-in to keep in the Keychain after any sign-in
// step.
func (c *Client) ExportSession() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return encodeJSON(c.session)
}

// Status says how far sign-in has got.
func (c *Client) Status() (string, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	return encodeJSON(status{
		SignedIn: s.AccessToken != "", Ready: s.AccessToken != "" && s.AccountID != "" && s.RoleName != "",
		AccountID: s.AccountID, RoleName: s.RoleName, Region: s.Request.AWSRegion, StartURL: s.Request.StartURL,
	})
}

// BeginSignIn starts a device authorization; Swift opens its URL.
func (c *Client) BeginSignIn(startURL, ssoRegion, awsRegion string) (string, error) {
	request := signInRequest{
		StartURL:  strings.TrimSpace(startURL),
		SSORegion: strings.TrimSpace(ssoRegion),
		AWSRegion: strings.TrimSpace(awsRegion),
	}
	if !strings.HasPrefix(request.StartURL, "https://") {
		return "", errors.New("enter the https start URL of your AWS access portal")
	}
	if request.SSORegion == "" || request.AWSRegion == "" {
		return "", errors.New("enter the Identity Center region and the region your sessions run in")
	}
	pending, output, err := beginDeviceAuthorization(context.Background(), request)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.pending = pending
	c.mu.Unlock()
	return encodeJSON(output)
}

// CompleteSignIn waits until the user approves the device (or the code
// expires), then lists their accounts. Call it off the main thread.
func (c *Client) CompleteSignIn() (string, error) {
	c.mu.Lock()
	pending := c.pending
	previous := c.session
	c.mu.Unlock()
	if pending == nil {
		return "", errors.New("start signing in first")
	}
	s, err := completeDeviceAuthorization(context.Background(), pending)
	if err != nil {
		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
		return "", err
	}
	accounts, err := listAccounts(context.Background(), s)
	if err != nil {
		return "", err
	}
	if candidate, ok := selectionForReauthorization(previous, s, accounts); ok {
		if validateCaller(context.Background(), candidate) == nil {
			s = candidate
		}
	}
	c.mu.Lock()
	c.session = s
	c.pending = nil
	c.config = nil
	c.mu.Unlock()
	return encodeJSON(accounts)
}

// selectionForReauthorization keeps the account and permission set picked
// before, when signing in again to the same portal still offers them.
func selectionForReauthorization(previous, current sessionState, accounts []account) (sessionState, bool) {
	if previous.AccountID == "" || previous.RoleName == "" || previous.Request != current.Request {
		return current, false
	}
	for _, a := range accounts {
		if a.ID == previous.AccountID {
			current.AccountID = previous.AccountID
			current.RoleName = previous.RoleName
			return current, true
		}
	}
	return current, false
}

// ListRoles lists the permission sets the user has in an account.
func (c *Client) ListRoles(accountID string) (string, error) {
	s, err := c.validSession(context.Background(), false)
	if err != nil {
		return "", err
	}
	roles, err := listRoles(context.Background(), s, accountID)
	if err != nil {
		return "", err
	}
	return encodeJSON(roles)
}

// FinishSetup picks the account and permission set pier works as.
func (c *Client) FinishSetup(accountID, roleName string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(roleName) == "" {
		return errors.New("pick an AWS account and permission set")
	}
	s, err := c.validSession(context.Background(), false)
	if err != nil {
		return err
	}
	s.AccountID, s.RoleName = accountID, roleName
	if err := validateCaller(context.Background(), s); err != nil {
		return err
	}
	c.mu.Lock()
	c.session = s
	c.config = nil
	c.mu.Unlock()
	return nil
}

// RefreshSignIn renews the access token now.
func (c *Client) RefreshSignIn() error {
	_, err := c.validSession(context.Background(), true)
	return err
}

// SignOut forgets the sign-in and every connection; Swift deletes the
// Keychain copy.
func (c *Client) SignOut() error {
	c.mu.Lock()
	remotes := c.remotes
	c.session = sessionState{}
	c.pending = nil
	c.reset()
	c.mu.Unlock()
	for _, r := range remotes {
		_ = r.close()
	}
	return nil
}

// ListSessions lists the user's sessions, by repo then name.
func (c *Client) ListSessions() (string, error) {
	items, err := c.loadSessions(context.Background())
	if err != nil {
		return "", err
	}
	out := make([]session, 0, len(items))
	for _, s := range items {
		out = append(out, s.Model)
	}
	return encodeJSON(out)
}

// Inspect reads what a running session is doing; a parked one answers with
// what EC2 knows.
func (c *Client) Inspect(id string) (string, error) {
	ctx := context.Background()
	r, err := c.remote(ctx, id)
	if err != nil {
		return "", err
	}
	if r.Model.State != "running" {
		return encodeJSON(snapshot{Session: r.Model, Windows: []window{}, Ports: []port{}, Chats: []chat{}})
	}
	out, err := c.runRemote(ctx, id, inspectScript())
	if err != nil {
		return "", err
	}
	return encodeJSON(parseInspection(r.Model, out))
}

// Wake starts a parked session and waits for EC2 to call it running; ssh
// follows a little later, as sshd comes up.
func (c *Client) Wake(id string) error {
	ctx := context.Background()
	r, err := c.remote(ctx, id)
	if err != nil {
		return err
	}
	if r.Model.State != "parked" {
		return nil
	}
	config, _, err := c.aws(ctx)
	if err != nil {
		return err
	}
	c.forget(id)
	if err := startInstance(ctx, config, id); err != nil {
		return err
	}
	_, err = c.loadSessions(ctx)
	return err
}

// Park stops a session now; only its disk costs while it's parked.
func (c *Client) Park(id string) error {
	ctx := context.Background()
	config, _, err := c.aws(ctx)
	if err != nil {
		return err
	}
	c.forget(id)
	return stopInstance(ctx, config, id)
}

// Remove destroys a session and its disk.
func (c *Client) Remove(id string) error {
	ctx := context.Background()
	config, _, err := c.aws(ctx)
	if err != nil {
		return err
	}
	if err := terminateInstance(ctx, config, id); err != nil {
		return err
	}
	c.forget(id)
	c.mu.Lock()
	delete(c.sessions, id)
	c.mu.Unlock()
	return nil
}

// NewWindow opens a tmux window in the session's checkout, running the
// command in commandJSON (a JSON array; empty for a shell).
func (c *Client) NewWindow(id, name, commandJSON string) (string, error) {
	var command []string
	if strings.TrimSpace(commandJSON) != "" {
		if err := json.Unmarshal([]byte(commandJSON), &command); err != nil {
			return "", fmt.Errorf("bad command: %w", err)
		}
	}
	ctx := context.Background()
	r, err := c.remote(ctx, id)
	if err != nil {
		return "", err
	}
	w, err := c.newWindow(ctx, r.Model, name, command)
	if err != nil {
		return "", err
	}
	return encodeJSON(w)
}

// ChatWindow opens the session's chats in tmux (see chatWindowScript) and
// returns the window to open a terminal on.
func (c *Client) ChatWindow(id, chatID string, takeover bool) (string, error) {
	out, err := c.runRemote(context.Background(), id, chatWindowScript(chatID, takeover))
	if err != nil {
		return "", err
	}
	w := strings.TrimSpace(out)
	if !windowID.MatchString(w) {
		return "", fmt.Errorf("the session didn't say which window has its chats (%q)", w)
	}
	return w, nil
}

// CloseWindow kills a tmux window and what runs in it.
func (c *Client) CloseWindow(id, window string) error {
	if !windowID.MatchString(window) {
		return fmt.Errorf("bad window id %q", window)
	}
	_, err := c.runRemote(context.Background(), id, "tmux kill-window -t "+shellQuote(window))
	return err
}

// forget drops a session's connection: its address changes as it parks.
func (c *Client) forget(id string) {
	c.mu.Lock()
	r := c.remotes[id]
	delete(c.remotes, id)
	c.mu.Unlock()
	if r != nil {
		_ = r.close()
	}
}

func (c *Client) loadSessions(ctx context.Context) ([]remoteSession, error) {
	config, me, err := c.aws(ctx)
	if err != nil {
		return nil, err
	}
	items, err := loadSessions(ctx, config, me)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sessions = make(map[string]remoteSession, len(items))
	for _, s := range items {
		c.sessions[s.Model.ID] = s
	}
	c.mu.Unlock()
	return items, nil
}

func (c *Client) remote(ctx context.Context, id string) (remoteSession, error) {
	c.mu.Lock()
	r, ok := c.sessions[id]
	c.mu.Unlock()
	if ok {
		return r, nil
	}
	if _, err := c.loadSessions(ctx); err != nil {
		return remoteSession{}, err
	}
	c.mu.Lock()
	r, ok = c.sessions[id]
	c.mu.Unlock()
	if !ok {
		return remoteSession{}, fmt.Errorf("no session %q", id)
	}
	return r, nil
}

// aws is the role's config and who it is, kept until its credentials are
// close to expiring.
func (c *Client) aws(ctx context.Context) (aws.Config, string, error) {
	c.mu.Lock()
	if c.config != nil && time.Now().Before(c.configTil) {
		config, me := *c.config, c.me
		c.mu.Unlock()
		return config, me, nil
	}
	c.mu.Unlock()
	s, err := c.configuredSession(ctx)
	if err != nil {
		return aws.Config{}, "", err
	}
	config, expires, err := authenticatedConfig(ctx, s)
	if err != nil {
		return aws.Config{}, "", err
	}
	me, err := callerARN(ctx, config)
	if err != nil {
		return aws.Config{}, "", err
	}
	c.mu.Lock()
	c.config, c.me, c.configTil = &config, me, expires.Add(-5*time.Minute)
	c.mu.Unlock()
	return config, me, nil
}

func (c *Client) validSession(ctx context.Context, force bool) (sessionState, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s.AccessToken == "" {
		return sessionState{}, errors.New("sign in to AWS first")
	}
	if force || time.Now().Add(2*time.Minute).Unix() >= s.AccessTokenExpiresAt {
		refreshed, err := refreshAccessToken(ctx, s)
		if err != nil {
			return sessionState{}, err
		}
		s = refreshed
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	return s, nil
}

func (c *Client) configuredSession(ctx context.Context) (sessionState, error) {
	s, err := c.validSession(ctx, false)
	if err != nil {
		return sessionState{}, err
	}
	if s.AccountID == "" || s.RoleName == "" {
		return sessionState{}, errors.New("pick an AWS account and permission set first")
	}
	return s, nil
}

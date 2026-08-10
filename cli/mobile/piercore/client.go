package piercore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client owns the authenticated Pier session. Swift persists ExportSession in
// Keychain; the Go framework never writes credentials to the app container.
type Client struct {
	mu        sync.Mutex
	session   sessionState
	pending   *pendingAuthorization
	instances map[string]remoteInstance
	remotes   map[string]*remoteClient
}

// NewClient restores a client from Keychain JSON. Pass an empty string before
// the first sign-in.
func NewClient(sessionJSON string) (*Client, error) {
	client := &Client{
		instances: make(map[string]remoteInstance),
		remotes:   make(map[string]*remoteClient),
	}
	if strings.TrimSpace(sessionJSON) != "" {
		if err := json.Unmarshal([]byte(sessionJSON), &client.session); err != nil {
			return nil, fmt.Errorf("restore Pier credentials: %w", err)
		}
	}
	return client, nil
}

// ExportSession returns the opaque credential state Swift should save in the
// iOS Keychain after authentication operations.
func (c *Client) ExportSession() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return encodeJSON(c.session)
}

func (c *Client) SetupStatus() (string, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	configured := session.AccountID != "" && session.RoleName != ""
	profiles := []string{}
	if configured {
		profiles = append(profiles, session.AccountID+" · "+session.RoleName)
	}
	return encodeJSON(setupStatus{
		Configured: configured,
		ConfigPath: func() string {
			if configured {
				return "iOS Keychain"
			}
			return "Not configured"
		}(),
		CLIVersion:   "Embedded PierCore",
		Profiles:     profiles,
		Dependencies: []dependency{},
	})
}

func (c *Client) BeginSignIn(startURL, ssoRegion, awsRegion string) (string, error) {
	request := signInRequest{
		StartURL:  strings.TrimSpace(startURL),
		SSORegion: strings.TrimSpace(ssoRegion),
		AWSRegion: strings.TrimSpace(awsRegion),
	}
	if !strings.HasPrefix(request.StartURL, "https://") {
		return "", errors.New("enter the HTTPS start URL from your AWS access portal")
	}
	if request.SSORegion == "" || request.AWSRegion == "" {
		return "", errors.New("enter the Identity Center and Pier instance regions")
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

// CompleteSignIn polls until the user authorizes the device or the AWS device
// code expires. Call it from a Swift Task so the UI remains responsive.
func (c *Client) CompleteSignIn() (string, error) {
	c.mu.Lock()
	pending := c.pending
	c.mu.Unlock()
	if pending == nil {
		return "", errors.New("start AWS sign-in first")
	}

	session, err := completeDeviceAuthorization(context.Background(), pending)
	if err != nil {
		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
		return "", err
	}
	accounts, err := listAccounts(context.Background(), session)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.session = session
	c.pending = nil
	c.mu.Unlock()
	return encodeJSON(accounts)
}

func (c *Client) ListRoles(accountID string) (string, error) {
	session, err := c.validSession(context.Background(), false)
	if err != nil {
		return "", err
	}
	roles, err := listRoles(context.Background(), session, accountID)
	if err != nil {
		return "", err
	}
	return encodeJSON(roles)
}

func (c *Client) FinishSetup(accountID, roleName string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(roleName) == "" {
		return errors.New("select an AWS account and permission set")
	}
	session, err := c.validSession(context.Background(), false)
	if err != nil {
		return err
	}
	session.AccountID = accountID
	session.RoleName = roleName
	if err := validateCaller(context.Background(), session); err != nil {
		return err
	}
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	return nil
}

func (c *Client) RefreshSignIn() error {
	_, err := c.validSession(context.Background(), true)
	return err
}

func (c *Client) ListInstances() (string, error) {
	items, err := c.loadInstances(context.Background())
	if err != nil {
		return "", err
	}
	models := make([]instance, 0, len(items))
	for _, item := range items {
		models = append(models, item.Model)
	}
	return encodeJSON(models)
}

func (c *Client) ListProjects() (string, error) {
	items, err := c.loadInstances(context.Background())
	if err != nil {
		return "", err
	}
	models := make([]instance, 0, len(items))
	for _, item := range items {
		models = append(models, item.Model)
	}
	return encodeJSON(projectsFromInstances(models))
}

func (c *Client) ListBranches(projectID string) (string, error) {
	items, err := c.loadInstances(context.Background())
	if err != nil {
		return "", err
	}
	branches := make(map[string]bool)
	projectName := strings.TrimPrefix(projectID, "aws:")
	for _, item := range items {
		if item.Model.ProjectID == projectID || projectName == projectNameFromInstance(item.Model) {
			if item.Model.Branch != "" {
				branches[item.Model.Branch] = true
			}
		}
	}
	if len(branches) == 0 {
		return "", errors.New("this project has no Pier sessions in AWS yet")
	}
	values := make([]string, 0, len(branches))
	for branch := range branches {
		values = append(values, branch)
	}
	sort.Strings(values)
	return encodeJSON(branchOptions{
		Project:       project{ID: projectID, Name: projectName, Path: "AWS · " + projectName},
		Branches:      values,
		DefaultBranch: values[0],
		FetchWarning:  "Creating the first mobile-only session for a repository still needs its source snapshot from the Mac.",
	})
}

func projectNameFromInstance(item instance) string { return projectName(item.Repo) }

func (c *Client) RemoveInstance(id string) error {
	session, err := c.configuredSession(context.Background())
	if err != nil {
		return err
	}
	if err := terminateInstance(context.Background(), session, id); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.instances, id)
	remote := c.remotes[id]
	delete(c.remotes, id)
	c.mu.Unlock()
	if remote != nil {
		_ = remote.close()
	}
	return nil
}

// UnparkInstance starts a stopped Pier VM and waits until EC2 reports it as
// running. SSH readiness is checked by the app's normal inspection loop.
func (c *Client) UnparkInstance(id string) error {
	remote, err := c.remote(context.Background(), id)
	if err != nil {
		return err
	}
	if remote.Model.State != "parked" {
		return nil
	}
	session, err := c.configuredSession(context.Background())
	if err != nil {
		return err
	}

	c.mu.Lock()
	client := c.remotes[id]
	delete(c.remotes, id)
	c.mu.Unlock()
	if client != nil {
		_ = client.close()
	}

	if err := startInstance(context.Background(), session, id); err != nil {
		return err
	}
	_, err = c.loadInstances(context.Background())
	return err
}

func (c *Client) InspectInstance(id string) (string, error) {
	remote, err := c.remote(context.Background(), id)
	if err != nil {
		return "", err
	}
	if remote.Model.State == "parked" {
		return encodeJSON(snapshot{Instance: remote.Model, Tabs: []tab{}, Ports: []port{}})
	}
	output, err := c.runRemote(context.Background(), id, inspectionCommand())
	if err != nil {
		return "", err
	}
	result, err := parseInspection(remote.Model, output)
	if err != nil {
		return "", err
	}
	return encodeJSON(result)
}

var validTabName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var validTabID = regexp.MustCompile(`^@[0-9]+$`)

func (c *Client) CreateTab(instanceID, name, commandJSON string) (string, error) {
	if !validTabName.MatchString(name) {
		return "", errors.New("tab name must use letters, numbers, dots, dashes, or underscores")
	}
	var command []string
	if err := json.Unmarshal([]byte(commandJSON), &command); err != nil || len(command) == 0 {
		return "", errors.New("tab command cannot be empty")
	}
	remote, err := c.remote(context.Background(), instanceID)
	if err != nil {
		return "", err
	}
	workingDirectory := "/home/agent/work/" + remote.Model.Repo
	window := []string{"tmux", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "main", "-n", name, "-c", workingDirectory, "--"}
	window = append(window, command...)
	first := []string{"tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", "main", "-n", name, "-c", workingDirectory, "--"}
	first = append(first, command...)
	remoteCommand := "if tmux has-session -t main 2>/dev/null; then " + shellJoin(window) + "; else " + shellJoin(first) + "; fi"
	output, err := c.runRemote(context.Background(), instanceID, remoteCommand)
	if err != nil {
		return "", err
	}
	return encodeJSON(tab{
		ID: strings.TrimSpace(output), Name: name, Panes: 1,
		Command: command[0], WorkingDirectory: workingDirectory,
	})
}

func (c *Client) CloseTab(instanceID, tabID string) error {
	if !validTabID.MatchString(tabID) {
		return fmt.Errorf("invalid tmux window id %q", tabID)
	}
	_, err := c.runRemote(context.Background(), instanceID, "tmux kill-window -t "+shellQuote(tabID)+" 2>/dev/null || true")
	return err
}

func (c *Client) validSession(ctx context.Context, force bool) (sessionState, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session.AccessToken == "" {
		return sessionState{}, errors.New("sign in to AWS first")
	}
	if force || time.Now().Add(2*time.Minute).Unix() >= session.AccessTokenExpiresAt {
		refreshed, err := refreshAccessToken(ctx, session)
		if err != nil {
			return sessionState{}, err
		}
		session = refreshed
		c.mu.Lock()
		c.session = session
		c.mu.Unlock()
	}
	return session, nil
}

func (c *Client) configuredSession(ctx context.Context) (sessionState, error) {
	session, err := c.validSession(ctx, false)
	if err != nil {
		return sessionState{}, err
	}
	if session.AccountID == "" || session.RoleName == "" {
		return sessionState{}, errors.New("finish AWS setup first")
	}
	return session, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for index, part := range parts {
		quoted[index] = shellQuote(part)
	}
	return strings.Join(quoted, " ")
}

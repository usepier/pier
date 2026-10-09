// Package github reads pull requests and their checks for sessions'
// branches, and acts on them: re-run failed jobs, read a job's log, merge
// with a guard on the head that was reviewed. It talks to GitHub's API with
// the laptop's own credential (gh's login, else git's), and to GitHub
// Enterprise hosts the same way, by the host in the session's origin.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrHeadMoved: the pull request's head is not the commit the merge was
// asked for.
var ErrHeadMoved = errors.New("the pull request changed since it was reviewed")

// ErrNoToken: no GitHub credential on this machine.
var ErrNoToken = errors.New("no GitHub login on this machine — run `gh auth login`")

// ErrUnauthorized: GitHub refused this machine's login (expired, revoked).
var ErrUnauthorized = errors.New("GitHub refused the login on this machine — run `gh auth login`")

// ErrRateLimited: this login's GitHub rate limit is used up for now.
var ErrRateLimited = errors.New("GitHub's rate limit for this login is used up")

// Error is GitHub refusing a request: its HTTP status (0 for a GraphQL
// error in an answer that otherwise succeeded), the GraphQL error type
// (NOT_FOUND, FORBIDDEN, RATE_LIMITED, ...) and what it said.
type Error struct {
	Status      int
	Type        string
	Message     string
	rateLimited bool
}

func (e *Error) Error() string { return "GitHub: " + e.Message }

// Is makes a refused login ErrUnauthorized and a spent rate limit
// ErrRateLimited.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrRateLimited:
		return e.rateLimited || e.Type == "RATE_LIMITED" || e.Status == http.StatusTooManyRequests
	}
	return false
}

// Repo names a repository by its canonical origin: host/owner/name.
type Repo struct{ Host, Owner, Name string }

// ParseOrigin reads "github.com/owner/name".
func ParseOrigin(origin string) (Repo, bool) {
	parts := strings.Split(strings.Trim(origin, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Repo{}, false
	}
	return Repo{Host: parts[0], Owner: parts[1], Name: parts[2]}, true
}

func (r Repo) String() string { return r.Host + "/" + r.Owner + "/" + r.Name }

// Client calls GitHub. Tokens are looked up once per host and kept a while.
type Client struct {
	HTTP *http.Client
	// Token finds the credential for a host; nil uses gh, then git.
	Token func(host string) string
	// API overrides the API base for every host (tests).
	API string

	mu     sync.Mutex
	tokens map[string]tokenAt
	limit  RateLimit
}

type tokenAt struct {
	token string
	at    time.Time
}

// RateLimit is what GitHub said was left of the hour's budget.
type RateLimit struct {
	Remaining int
	ResetAt   time.Time
}

func (c *Client) token(host string) string {
	c.mu.Lock()
	if t, ok := c.tokens[host]; ok && time.Since(t.at) < 10*time.Minute {
		c.mu.Unlock()
		return t.token
	}
	c.mu.Unlock()
	var t string
	if c.Token != nil {
		t = c.Token(host)
	} else if out, err := exec.Command("gh", "auth", "token", "--hostname", host).Output(); err == nil {
		t = strings.TrimSpace(string(out))
	}
	c.mu.Lock()
	if c.tokens == nil {
		c.tokens = map[string]tokenAt{}
	}
	c.tokens[host] = tokenAt{t, time.Now()}
	c.mu.Unlock()
	return t
}

func (c *Client) base(host string) (graphql, rest string) {
	if c.API != "" {
		return c.API + "/graphql", c.API
	}
	if host == "github.com" {
		return "https://api.github.com/graphql", "https://api.github.com"
	}
	return "https://" + host + "/api/graphql", "https://" + host + "/api/v3"
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Limit is the rate limit GitHub last reported.
func (c *Client) Limit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit
}

func (c *Client) do(ctx context.Context, host, method, url string, body any, out any) error {
	tok := c.token(host)
	if tok == "" {
		return ErrNoToken
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	spent := false
	if n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil {
		reset, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		c.mu.Lock()
		c.limit = RateLimit{Remaining: n, ResetAt: time.Unix(reset, 0)}
		c.mu.Unlock()
		spent = n == 0
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		delete(c.tokens, host)
		c.mu.Unlock()
		return &Error{Status: resp.StatusCode, Message: "refused the login on this machine (" + resp.Status + ") — run `gh auth login`"}
	}
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return &Error{Status: resp.StatusCode, Message: e.Message,
			rateLimited: spent && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests)}
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) graphql(ctx context.Context, host, query string, vars map[string]any, out any) error {
	url, _ := c.base(host)
	var resp struct {
		Data   json.RawMessage
		Errors []struct {
			Type    string
			Message string
		}
	}
	if err := c.do(ctx, host, http.MethodPost, url, map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	empty := len(resp.Data) == 0 || string(resp.Data) == "null"
	switch {
	case empty && len(resp.Errors) > 0:
		return &Error{Type: resp.Errors[0].Type, Message: resp.Errors[0].Message}
	case empty:
		return &Error{Message: "an empty answer"}
	case len(resp.Errors) > 0 && out == nil:
		// A mutation that reported errors didn't happen.
		return &Error{Type: resp.Errors[0].Type, Message: resp.Errors[0].Message}
	}
	// A query with partial errors (one branch's lookup failing) still
	// answers the rest.
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Data, out)
}

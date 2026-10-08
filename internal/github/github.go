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
	"fmt"
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
	if n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil {
		reset, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		c.mu.Lock()
		c.limit = RateLimit{Remaining: n, ResetAt: time.Unix(reset, 0)}
		c.mu.Unlock()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		delete(c.tokens, host)
		c.mu.Unlock()
		return fmt.Errorf("GitHub refused the login on this machine (%s) — run `gh auth login`", resp.Status)
	}
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return fmt.Errorf("GitHub: %s", e.Message)
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
		return fmt.Errorf("GitHub: %s", resp.Errors[0].Message)
	case empty:
		return errors.New("GitHub: an empty answer")
	case len(resp.Errors) > 0 && out == nil:
		// A mutation that reported errors didn't happen.
		return fmt.Errorf("GitHub: %s", resp.Errors[0].Message)
	}
	// A query with partial errors (one branch's lookup failing) still
	// answers the rest.
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Data, out)
}

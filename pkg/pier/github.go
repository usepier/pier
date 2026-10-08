package pier

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"

	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/github"
)

// Pull requests and checks for sessions' branches, read from GitHub with
// this machine's own login.

type (
	PR       = github.PR
	PRCheck  = github.Check
	PRDetail = github.Detail
)

// ErrHeadMoved: a merge was asked for a head the pull request has moved
// past.
var ErrHeadMoved = github.ErrHeadMoved

// ErrNoPR: the session's branch has no pull request.
var ErrNoPR = errors.New("this branch has no pull request")

var (
	ghOnce   sync.Once
	ghClient *github.Client
)

// gh is the process's GitHub client: one token cache and one rate limit.
func gh() *github.Client {
	ghOnce.Do(func() {
		ghClient = &github.Client{Token: func(host string) string {
			if host == "github.com" {
				return payload.GitHubToken()
			}
			out, err := exec.Command("gh", "auth", "token", "--hostname", host).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}}
	})
	return ghClient
}

// GitHubLimit is how much of GitHub's hourly budget is left.
func (c *Client) GitHubLimit() github.RateLimit { return gh().Limit() }

// PullRequests finds each session's pull request by its branch, one query
// per repository. Sessions without a GitHub origin are left out.
func (c *Client) PullRequests(ctx context.Context, sessions []Session) (map[string]PR, error) {
	byRepo := map[github.Repo][]Session{}
	for _, s := range sessions {
		if s.PoolGen != "" || s.Branch == "" {
			continue
		}
		r, ok := github.ParseOrigin(s.Origin)
		if !ok {
			continue
		}
		byRepo[r] = append(byRepo[r], s)
	}
	out := map[string]PR{}
	var firstErr error
	for r, ss := range byRepo {
		var branches []string
		seen := map[string]bool{}
		for _, s := range ss {
			if !seen[s.Branch] {
				seen[s.Branch] = true
				branches = append(branches, s.Branch)
			}
		}
		prs, err := gh().ForBranches(ctx, r, branches)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, s := range ss {
			if p, ok := prs[s.Branch]; ok {
				out[s.ID] = p
			}
		}
	}
	return out, firstErr
}

func repoOf(s Session) (github.Repo, error) {
	r, ok := github.ParseOrigin(s.Origin)
	if !ok {
		return github.Repo{}, errors.New("this session's repository isn't on GitHub")
	}
	return r, nil
}

// PullRequest reads a session's pull request in full: checks, review
// threads, reviews.
func (c *Client) PullRequest(ctx context.Context, s Session, number int) (PRDetail, error) {
	r, err := repoOf(s)
	if err != nil {
		return PRDetail{}, err
	}
	return gh().Get(ctx, r, number)
}

// MergePR merges a session's pull request if its head is still
// expectedHead (ErrHeadMoved otherwise).
func (c *Client) MergePR(ctx context.Context, s Session, prID, method, expectedHead string) error {
	r, err := repoOf(s)
	if err != nil {
		return err
	}
	return gh().Merge(ctx, r, prID, method, expectedHead)
}

// RerunChecks re-runs a workflow run's failed jobs.
func (c *Client) RerunChecks(ctx context.Context, s Session, runID int64) error {
	r, err := repoOf(s)
	if err != nil {
		return err
	}
	return gh().Rerun(ctx, r, runID)
}

// CheckLog is the end of a check's job log (GitHub Actions).
func (c *Client) CheckLog(ctx context.Context, s Session, jobID int64, maxBytes int) (string, error) {
	r, err := repoOf(s)
	if err != nil {
		return "", err
	}
	return gh().JobLog(ctx, r, jobID, maxBytes)
}

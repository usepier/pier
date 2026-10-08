package github

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PR is a pull request as the sidebar and the PR panel show it.
type PR struct {
	ID        string // GraphQL node id, for mutations
	Number    int
	Title     string
	URL       string
	State     string // open | draft | merged | closed
	HeadOid   string
	HeadRef   string
	BaseRef   string
	Additions int
	Deletions int
	UpdatedAt time.Time
	// Review: approved | changes_requested | review_required | "".
	Review string
	// Mergeable: clean | conflicts | blocked | behind | unstable | unknown.
	Mergeable string
	Checks    []Check
}

// Check is one check run or commit status on the PR's head.
type Check struct {
	Name string
	// State: pending | passing | failing | skipped.
	State string
	URL   string
	JobID int64 // GitHub Actions: the job, for its log
	RunID int64 // GitHub Actions: the workflow run, for re-runs
}

// ChecksState sums the checks up: failing if any fails, else pending if
// any runs, else passing; "" with none.
func (p PR) ChecksState() string {
	if len(p.Checks) == 0 {
		return ""
	}
	pending := false
	for _, c := range p.Checks {
		switch c.State {
		case "failing":
			return "failing"
		case "pending":
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "passing"
}

const prFields = `id number title url state isDraft merged mergeable mergeStateStatus reviewDecision
headRefOid headRefName baseRefName additions deletions updatedAt
headRepositoryOwner { login }
commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
  __typename
  ... on CheckRun { name status conclusion detailsUrl databaseId checkSuite { workflowRun { databaseId } } }
  ... on StatusContext { context state targetUrl }
} } } } } }`

type prNode struct {
	ID                  string
	Number              int
	Title               string
	URL                 string
	State               string
	IsDraft             bool
	Merged              bool
	Mergeable           string
	MergeStateStatus    string
	ReviewDecision      string
	HeadRefOid          string
	HeadRefName         string
	BaseRefName         string
	Additions           int
	Deletions           int
	UpdatedAt           time.Time
	HeadRepositoryOwner *struct{ Login string }
	Commits             struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkNode
					}
				}
			}
		}
	}
}

type checkNode struct {
	Typename   string `json:"__typename"`
	Name       string
	Status     string
	Conclusion string
	DetailsURL string
	DatabaseID int64
	CheckSuite *struct {
		WorkflowRun *struct{ DatabaseID int64 }
	}
	Context   string
	State     string
	TargetURL string
}

func (n prNode) pr() PR {
	p := PR{ID: n.ID, Number: n.Number, Title: n.Title, URL: n.URL, HeadOid: n.HeadRefOid, HeadRef: n.HeadRefName,
		BaseRef: n.BaseRefName, Additions: n.Additions, Deletions: n.Deletions, UpdatedAt: n.UpdatedAt}
	switch {
	case n.Merged || n.State == "MERGED":
		p.State = "merged"
	case n.State == "CLOSED":
		p.State = "closed"
	case n.IsDraft:
		p.State = "draft"
	default:
		p.State = "open"
	}
	p.Review = strings.ToLower(n.ReviewDecision)
	switch {
	case n.Mergeable == "CONFLICTING" || n.MergeStateStatus == "DIRTY":
		p.Mergeable = "conflicts"
	case n.MergeStateStatus == "CLEAN" || n.MergeStateStatus == "HAS_HOOKS":
		p.Mergeable = "clean"
	case n.MergeStateStatus == "BLOCKED":
		p.Mergeable = "blocked"
	case n.MergeStateStatus == "BEHIND":
		p.Mergeable = "behind"
	case n.MergeStateStatus == "UNSTABLE":
		p.Mergeable = "unstable"
	default:
		p.Mergeable = "unknown"
	}
	for _, c := range n.Commits.Nodes {
		if c.Commit.StatusCheckRollup == nil {
			continue
		}
		for _, x := range c.Commit.StatusCheckRollup.Contexts.Nodes {
			p.Checks = append(p.Checks, x.check())
		}
	}
	return p
}

func (x checkNode) check() Check {
	if x.Typename == "StatusContext" {
		st := map[string]string{"SUCCESS": "passing", "FAILURE": "failing", "ERROR": "failing"}[x.State]
		if st == "" {
			st = "pending"
		}
		return Check{Name: x.Context, State: st, URL: x.TargetURL}
	}
	c := Check{Name: x.Name, URL: x.DetailsURL, JobID: x.DatabaseID}
	if x.CheckSuite != nil && x.CheckSuite.WorkflowRun != nil {
		c.RunID = x.CheckSuite.WorkflowRun.DatabaseID
	}
	switch {
	case x.Status != "COMPLETED":
		c.State = "pending"
	case x.Conclusion == "SUCCESS" || x.Conclusion == "NEUTRAL":
		c.State = "passing"
	case x.Conclusion == "SKIPPED":
		c.State = "skipped"
	default:
		c.State = "failing"
	}
	return c
}

// ForBranches finds the newest pull request (open, else merged or closed)
// for each of a repository's branches, in one query.
func (c *Client) ForBranches(ctx context.Context, r Repo, branches []string) (map[string]PR, error) {
	out := map[string]PR{}
	for start := 0; start < len(branches); start += 25 {
		end := min(start+25, len(branches))
		var q strings.Builder
		vars := map[string]any{"owner": r.Owner, "name": r.Name}
		q.WriteString("query($owner: String!, $name: String!")
		for i := start; i < end; i++ {
			fmt.Fprintf(&q, ", $b%d: String!", i)
			vars[fmt.Sprintf("b%d", i)] = branches[i]
		}
		q.WriteString(") { repository(owner: $owner, name: $name) {")
		for i := start; i < end; i++ {
			fmt.Fprintf(&q, " b%d: pullRequests(headRefName: $b%d, first: 5, orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { %s } }", i, i, prFields)
		}
		q.WriteString(" } }")
		var data struct {
			Repository map[string]struct{ Nodes []prNode }
		}
		if err := c.graphql(ctx, r.Host, q.String(), vars, &data); err != nil {
			return nil, err
		}
		for i := start; i < end; i++ {
			if p, ok := pick(data.Repository[fmt.Sprintf("b%d", i)].Nodes, r.Owner); ok {
				out[branches[i]] = p
			}
		}
	}
	return out, nil
}

// pick prefers an open PR from the repository's own owner (not a fork with
// a branch of the same name), else the newest.
func pick(nodes []prNode, owner string) (PR, bool) {
	var best *prNode
	for i := range nodes {
		n := &nodes[i]
		if n.HeadRepositoryOwner != nil && !strings.EqualFold(n.HeadRepositoryOwner.Login, owner) {
			continue
		}
		if best == nil || (n.State == "OPEN" && best.State != "OPEN") {
			best = n
		}
	}
	if best == nil {
		return PR{}, false
	}
	return best.pr(), true
}

// Thread is a review conversation on a line.
type Thread struct {
	Path     string
	Line     int
	Resolved bool
	Outdated bool
	Comments []ThreadComment
}

// ThreadComment is one comment of a thread.
type ThreadComment struct {
	Author string
	Body   string
}

// Review is a reviewer's verdict.
type Review struct {
	Author string
	State  string // approved | changes_requested | commented
	Body   string
}

// Detail is a PR with its review conversations.
type Detail struct {
	PR
	Threads []Thread
	Reviews []Review
	Body    string
}

// Get reads one pull request in full.
func (c *Client) Get(ctx context.Context, r Repo, number int) (Detail, error) {
	q := `query($owner: String!, $name: String!, $n: Int!) { repository(owner: $owner, name: $name) { pullRequest(number: $n) {
` + prFields + ` body
reviewThreads(first: 50) { nodes { isResolved isOutdated path line comments(first: 20) { nodes { author { login } body } } } }
latestOpinionatedReviews(first: 20) { nodes { author { login } state body } }
} } }`
	var data struct {
		Repository struct {
			PullRequest *struct {
				prNode
				Body          string
				ReviewThreads struct {
					Nodes []struct {
						IsResolved bool
						IsOutdated bool
						Path       string
						Line       int
						Comments   struct {
							Nodes []struct {
								Author *struct{ Login string }
								Body   string
							}
						}
					}
				}
				LatestOpinionatedReviews struct {
					Nodes []struct {
						Author *struct{ Login string }
						State  string
						Body   string
					}
				}
			}
		}
	}
	if err := c.graphql(ctx, r.Host, q, map[string]any{"owner": r.Owner, "name": r.Name, "n": number}, &data); err != nil {
		return Detail{}, err
	}
	n := data.Repository.PullRequest
	if n == nil {
		return Detail{}, fmt.Errorf("no pull request #%d in %s", number, r)
	}
	d := Detail{PR: n.prNode.pr(), Body: n.Body}
	login := func(a *struct{ Login string }) string {
		if a == nil {
			return "ghost"
		}
		return a.Login
	}
	for _, t := range n.ReviewThreads.Nodes {
		th := Thread{Path: t.Path, Line: t.Line, Resolved: t.IsResolved, Outdated: t.IsOutdated}
		for _, cm := range t.Comments.Nodes {
			th.Comments = append(th.Comments, ThreadComment{Author: login(cm.Author), Body: cm.Body})
		}
		d.Threads = append(d.Threads, th)
	}
	for _, rv := range n.LatestOpinionatedReviews.Nodes {
		d.Reviews = append(d.Reviews, Review{Author: login(rv.Author), State: strings.ToLower(rv.State), Body: rv.Body})
	}
	return d, nil
}

// Merge merges a pull request — only if its head is still expectedHead,
// the commit that was reviewed (ErrHeadMoved otherwise). method: merge,
// squash or rebase.
func (c *Client) Merge(ctx context.Context, r Repo, prID, method, expectedHead string) error {
	m := map[string]string{"merge": "MERGE", "squash": "SQUASH", "rebase": "REBASE"}[method]
	if m == "" {
		return fmt.Errorf("unknown merge method %q", method)
	}
	if expectedHead == "" {
		return fmt.Errorf("a merge needs the head that was reviewed")
	}
	q := `mutation($id: ID!, $m: PullRequestMergeMethod!, $head: GitObjectID!) {
  mergePullRequest(input: {pullRequestId: $id, mergeMethod: $m, expectedHeadOid: $head}) { pullRequest { merged } } }`
	err := c.graphql(ctx, r.Host, q, map[string]any{"id": prID, "m": m, "head": expectedHead}, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "head branch was modified") {
		return ErrHeadMoved
	}
	return err
}

// Rerun re-runs a workflow run's failed jobs.
func (c *Client) Rerun(ctx context.Context, r Repo, runID int64) error {
	_, rest := c.base(r.Host)
	return c.do(ctx, r.Host, "POST", fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/rerun-failed-jobs", rest, r.Owner, r.Name, runID), struct{}{}, nil)
}

// JobLog is the end of a GitHub Actions job's log.
func (c *Client) JobLog(ctx context.Context, r Repo, jobID int64, maxBytes int) (string, error) {
	_, rest := c.base(r.Host)
	var log string
	if err := c.do(ctx, r.Host, "GET", fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d/logs", rest, r.Owner, r.Name, jobID), nil, &log); err != nil {
		return "", err
	}
	if len(log) > maxBytes {
		log = log[len(log)-maxBytes:]
		if i := strings.IndexByte(log, '\n'); i >= 0 {
			log = log[i+1:]
		}
	}
	return log, nil
}

package wire

// PRSummary is a session's pull request, as a row shows it.
type PRSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// State: open | draft | merged | closed
	State     string `json:"state"`
	HeadOid   string `json:"headOid"`
	BaseRef   string `json:"baseRef"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Checks: passing | failing | pending | "" (none)
	Checks       string `json:"checks"`
	ChecksPassed int    `json:"checksPassed"`
	ChecksTotal  int    `json:"checksTotal"`
	// Review: approved | changes_requested | review_required | ""
	Review string `json:"review"`
	// Mergeable: clean | conflicts | blocked | behind | unstable | unknown
	Mergeable string `json:"mergeable"`
}

// PRCheck is one check on a pull request's head.
type PRCheck struct {
	Name string `json:"name"`
	// State: pending | passing | failing | skipped
	State string `json:"state"`
	URL   string `json:"url"`
	// JobID reads the log (pr.log). GitHub Actions only.
	JobID int64 `json:"jobId,omitempty"`
}

// PRThread is a review conversation on a line.
type PRThread struct {
	Path     string      `json:"path"`
	Line     int         `json:"line"`
	Resolved bool        `json:"resolved"`
	Outdated bool        `json:"outdated"`
	Comments []PRComment `json:"comments"`
}

// PRComment is one comment of a thread.
type PRComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	// HTML is the body as GitHub renders it (markdown, its HTML, mentions).
	HTML string `json:"html,omitempty"`
}

// PRReview is a reviewer's verdict.
type PRReview struct {
	Author string `json:"author"`
	State  string `json:"state"` // approved | changes_requested | commented
	Body   string `json:"body"`
	// HTML is the body as GitHub renders it.
	HTML string `json:"html,omitempty"`
}

// PRDetail is a session's pull request in full.
type PRDetail struct {
	PRSummary
	// ID is GitHub's id for it, for pr.merge.
	ID      string     `json:"id"`
	Body    string     `json:"body"`
	Runs    []PRCheck  `json:"runs"`
	Threads []PRThread `json:"threads"`
	Reviews []PRReview `json:"reviews"`
}

// PRLogParams reads a check's log.
type PRLogParams struct {
	SessionID string `json:"sessionId"`
	JobID     int64  `json:"jobId"`
}

// PRMergeParams merges a session's pull request.
type PRMergeParams struct {
	SessionID string `json:"sessionId"`
	// ExpectedHeadOid is the head that was reviewed: if the pull request
	// moved past it, the merge fails with kind head_moved.
	ExpectedHeadOid string `json:"expectedHeadOid"`
}

// ComposeParams asks for a prompt for the agent.
type ComposeParams struct {
	SessionID string `json:"sessionId"`
	// Kind: fixCI | addressComments | createPR
	Kind string `json:"kind"`
}

// Composed is a prompt to put in the composer (not sent).
type Composed struct {
	Text string `json:"text"`
}

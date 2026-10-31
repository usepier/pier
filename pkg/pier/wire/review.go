package wire

import "time"

// ChangedFile is a file that differs from where the session's branch
// started.
type ChangedFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"` // renames
	// Status: added | modified | deleted | renamed | untracked | conflicted
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
	// Uncommitted: the working tree or index differs from HEAD here.
	Uncommitted bool `json:"uncommitted,omitempty"`
	// Stamp changes with every edit of the file (pushed changes only).
	Stamp string `json:"stamp,omitempty"`
}

// Changes is a session's checkout against its base: everything the branch
// changed, committed or not.
type Changes struct {
	Branch   string `json:"branch"` // "" when detached
	Head     string `json:"head"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`  // commits the upstream lacks
	Behind   int    `json:"behind"` // commits the upstream has that HEAD lacks
	// BaseRef is what the branch is compared with (origin/main); BaseOid
	// the commit it started from.
	BaseRef     string        `json:"baseRef,omitempty"`
	BaseOid     string        `json:"baseOid,omitempty"`
	Files       []ChangedFile `json:"files"`
	Uncommitted int           `json:"uncommitted"`
	// Live is false for a session that isn't running: what this machine
	// last read, as of AsOf.
	Live bool       `json:"live"`
	AsOf *time.Time `json:"asOf,omitempty"`
}

// DiffParams asks for one changed file's diff.
type DiffParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status,omitempty"`
	// Context is the lines around each change; 0 for the default.
	Context int `json:"context,omitempty"`
}

// FileDiff is a unified diff of one file against the base.
type FileDiff struct {
	Path      string     `json:"path"`
	Patch     string     `json:"patch"`
	Truncated bool       `json:"truncated,omitempty"`
	Live      bool       `json:"live"`
	AsOf      *time.Time `json:"asOf,omitempty"`
}

// FilePathParams names a file of a session's checkout.
type FilePathParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"` // relative to the repo root
}

// FileAtParams names a file of a session's checkout, as the working
// tree has it or (at "base") as the branch's base does.
type FileAtParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	At        string `json:"at,omitempty"` // "work" (default) | "base"
}

// FileContent is a file of a session's checkout. Text is set for text
// files; binary ones only say so.
type FileContent struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Binary bool   `json:"binary,omitempty"`
	Size   int64  `json:"size"`
	// Sha identifies the content read, for files.write's conflict check.
	Sha      string `json:"sha"`
	Missing  bool   `json:"missing,omitempty"`
	TooLarge bool   `json:"tooLarge,omitempty"`
	// AsOf is set for a session that can't be read now: the file as this
	// machine kept it then (read-only).
	AsOf *time.Time `json:"asOf,omitempty"`
}

// FileWriteParams saves a file, unless it changed since it was read.
type FileWriteParams struct {
	SessionID string `json:"sessionId"`
	// OpID makes the save happen once: retry with the same id.
	OpID string `json:"opId"`
	Path string `json:"path"`
	Text string `json:"text"`
	// BaseSha is the sha files.read gave ("" for a new file); a file that
	// changed since fails with kind "conflict".
	BaseSha string `json:"baseSha"`
}

// FileWritten is the saved file's new sha.
type FileWritten struct {
	Sha string `json:"sha"`
}

// SearchParams searches a session's checkout: literal text, or a regular
// expression in ripgrep's syntax.
type SearchParams struct {
	SessionID     string `json:"sessionId"`
	Query         string `json:"query"`
	Regex         bool   `json:"regex,omitempty"`
	CaseSensitive bool   `json:"caseSensitive,omitempty"`
}

// SearchMatch is one line that matched.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// SearchResult is a search's matches as they're found: the snapshot has
// none, each event the next batch; the last says done (and why it
// failed, if it did).
type SearchResult struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
	Done      bool          `json:"done"`
	Error     *ErrorData    `json:"error,omitempty"`
}

// CommitParams commits everything in a session's checkout.
type CommitParams struct {
	SessionID string `json:"sessionId"`
	OpID      string `json:"opId"`
	Message   string `json:"message"`
}

// CommitResult is the new HEAD.
type CommitResult struct {
	Head string `json:"head"`
}

// PushParams pushes a session's branch.
type PushParams struct {
	SessionID string `json:"sessionId"`
	OpID      string `json:"opId"`
}

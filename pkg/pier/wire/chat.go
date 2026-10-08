package wire

import "time"

// The chat is an agent's conversation, read from the transcript the agent
// CLI itself writes, as a list of items. Items arrive as the agent writes
// them — per finished message or tool step, not per token.

// ChatKind is what an item is.
type ChatKind string

const (
	ChatUser      ChatKind = "user"      // a prompt
	ChatAssistant ChatKind = "assistant" // the agent's text
	ChatThinking  ChatKind = "thinking"  // the agent's reasoning, when it shows it
	ChatTool      ChatKind = "tool"      // a tool call and its result
	ChatStatus    ChatKind = "status"    // something about the conversation: interrupted, compacted, a command ran
	ChatError     ChatKind = "error"     // the agent hit an error
	ChatUnknown   ChatKind = "unknown"   // a line this pier can't read yet
)

// ChatItem is one entry of the conversation.
type ChatItem struct {
	ID   string    `json:"id"`
	Kind ChatKind  `json:"kind"`
	Time time.Time `json:"time"`
	Text string    `json:"text,omitempty"`
	// Images counts images attached to a prompt.
	Images int       `json:"images,omitempty"`
	Model  string    `json:"model,omitempty"`
	Tool   *ToolCall `json:"tool,omitempty"`
	// Sidechain marks a subagent's item.
	Sidechain bool `json:"sidechain,omitempty"`
}

// ToolView is how a tool call is best shown.
type ToolView string

const (
	ToolEdit    ToolView = "edit"   // a file change, as hunks
	ToolWrite   ToolView = "write"  // a new file
	ToolBash    ToolView = "bash"   // a command and its output
	ToolRead    ToolView = "read"   // a file read
	ToolTodo    ToolView = "todo"   // the agent's task list
	ToolSearch  ToolView = "search" // grep, glob: what matched
	ToolWeb     ToolView = "web"    // a fetch or a search on the web
	ToolTask    ToolView = "task"   // a subagent
	ToolGeneric ToolView = "generic"
)

// ToolStatus is where a tool call is.
type ToolStatus string

const (
	ToolRunning     ToolStatus = "running"
	ToolOK          ToolStatus = "ok"
	ToolFailed      ToolStatus = "error"
	ToolDenied      ToolStatus = "denied"
	ToolInterrupted ToolStatus = "interrupted"
)

// ToolCall is one tool call with its outcome.
type ToolCall struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	View   ToolView   `json:"view"`
	Status ToolStatus `json:"status"`
	// Summary is the call in one line: the command, the file, the pattern.
	Summary string `json:"summary"`
	// Input is the call's arguments as JSON, trimmed.
	Input     string     `json:"input,omitempty"`
	Result    string     `json:"result,omitempty"` // trimmed
	Truncated bool       `json:"truncated,omitempty"`
	Edit      *EditView  `json:"edit,omitempty"`
	Bash      *BashView  `json:"bash,omitempty"`
	Todos     []TodoItem `json:"todos,omitempty"`
}

// EditView is a file change.
type EditView struct {
	Path    string `json:"path"`
	Created bool   `json:"created,omitempty"`
	Hunks   []Hunk `json:"hunks"`
}

// Hunk is a run of changed lines; each line starts with ' ', '-' or '+'.
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// BashView is a command and its output.
type BashView struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Output      string `json:"output,omitempty"` // stdout then stderr, trimmed
	ExitCode    *int   `json:"exitCode,omitempty"`
}

// TodoItem is one entry of the agent's task list.
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

// AgentDetail is the agent's full state for a chat: what AgentStatus says,
// the permission request waiting on you, and the prompts the session holds
// for it.
type AgentDetail struct {
	AgentStatus
	Model   string             `json:"model,omitempty"`
	Pending *PermissionRequest `json:"pending,omitempty"`
	Queue   []QueuedPrompt     `json:"queue"`
}

// QueuedPrompt is a prompt the session holds until its agent can take it.
type QueuedPrompt struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// PermissionRequest is the agent asking to use a tool.
type PermissionRequest struct {
	ID      string    `json:"id"`
	Tool    string    `json:"tool"`
	Summary string    `json:"summary"` // the call in one line
	Input   string    `json:"input,omitempty"`
	At      time.Time `json:"at"`
}

// ChatSnapshot is a chat as a watch begins.
type ChatSnapshot struct {
	Items []ChatItem `json:"items"`
	// More says older items exist (chat.history pages them in).
	More  bool         `json:"more"`
	Agent *AgentDetail `json:"agent,omitempty"`
	// Live is false for a session that isn't running: the items are what
	// this Mac last saw, as of AsOf.
	Live bool       `json:"live"`
	AsOf *time.Time `json:"asOf,omitempty"`
	// Partial says some of the transcript couldn't be read: the terminal
	// view shows the whole conversation.
	Partial bool `json:"partial,omitempty"`
}

// ChatOp says how an update applies.
type ChatOp string

const (
	ChatAppend ChatOp = "append" // new items at the end
	ChatUpdate ChatOp = "update" // items replacing ones with the same id
	ChatReset  ChatOp = "reset"  // the conversation started over: replace everything
	ChatAgent  ChatOp = "agent"  // only the agent's state changed
)

// ChatChange is one update of a chat watch.
type ChatChange struct {
	Op    ChatOp     `json:"op"`
	Items []ChatItem `json:"items,omitempty"`
	// More goes with a reset: older items exist.
	More    bool         `json:"more,omitempty"`
	Agent   *AgentDetail `json:"agent,omitempty"`
	Partial bool         `json:"partial,omitempty"`
	// Live and AsOf come with an agent change when the session's stream
	// comes or goes.
	Live *bool      `json:"live,omitempty"`
	AsOf *time.Time `json:"asOf,omitempty"`
}

// ChatSendParams sends a prompt to a session's agent.
type ChatSendParams struct {
	SessionID string `json:"sessionId"`
	// PromptID makes the send happen once: retry with the same id.
	PromptID string `json:"promptId"`
	Text     string `json:"text"`
	// Mode "now" types it in at once, even mid-turn; otherwise it waits for
	// the agent to be free.
	Mode string `json:"mode,omitempty"`
}

// ChatSendResult says how a prompt is on its way.
type ChatSendResult struct {
	// Job is set when the session was parked: a background job wakes it and
	// queues the prompt.
	Job *Job `json:"job,omitempty"`
}

// DecideParams answers a permission request.
type DecideParams struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Behavior  string `json:"behavior"` // allow | deny
	Message   string `json:"message,omitempty"`
}

// HistoryParams pages older chat items in.
type HistoryParams struct {
	SessionID string `json:"sessionId"`
	Before    string `json:"before"` // the oldest item id the client has
	Limit     int    `json:"limit,omitempty"`
}

// HistoryPage is a page of older items, oldest first.
type HistoryPage struct {
	Items []ChatItem `json:"items"`
	More  bool       `json:"more"`
}

// UploadParams puts a file into a session (an image pasted into the chat).
type UploadParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"` // its name, for the extension
	Data      string `json:"data"` // base64
}

// Uploaded is where an upload landed in the session.
type Uploaded struct {
	Path string `json:"path"`
}

// FileListParams lists a session's files.
type FileListParams struct {
	SessionID string `json:"sessionId"`
}

// FileList is the paths of a session's checkout (tracked and untracked,
// ignored ones left out), relative to the repo root.
type FileList struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// Notice is something worth telling the user about a session (notify):
// its agent finished or needs them, a setup or create failed.
type Notice struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // finished | needs_input | setup_failed | create_failed
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// PeekParams asks for the bottom of the agent's pane.
type PeekParams struct {
	SessionID string `json:"sessionId"`
	Lines     int    `json:"lines,omitempty"`
}

// Peek is the bottom of the agent's pane as plain text.
type Peek struct {
	Text string `json:"text"`
}

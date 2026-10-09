package wire

import "time"

// A session holds any number of chats, each a conversation with an agent
// (claude or codex) that the session's supervisor runs headless. A chat is
// a list of items: they arrive as the agent finishes them, and the text of
// the one it is writing streams in as deltas.

// ChatKind is what an item is.
type ChatKind string

const (
	ChatUser      ChatKind = "user"      // a prompt
	ChatAssistant ChatKind = "assistant" // the agent's text
	ChatThinking  ChatKind = "thinking"  // the agent's reasoning, when it shows it
	ChatTool      ChatKind = "tool"      // a tool call and its result
	ChatPlan      ChatKind = "plan"      // a plan the agent proposed
	ChatStatus    ChatKind = "status"    // something about the conversation: interrupted, compacted, a command ran
	ChatError     ChatKind = "error"     // the agent hit an error
)

// ChatItem is one entry of the conversation.
type ChatItem struct {
	ID   string    `json:"id"`
	Kind ChatKind  `json:"kind"`
	Time time.Time `json:"time"`
	Text string    `json:"text,omitempty"`
	// Attachments are the files that came with a prompt.
	Attachments []Attachment `json:"attachments,omitempty"`
	Model       string       `json:"model,omitempty"`
	Tool        *ToolCall    `json:"tool,omitempty"`
	// Sidechain marks a subagent's item.
	Sidechain bool `json:"sidechain,omitempty"`
}

// Attachment is a file that came with a prompt: its name, and where it is
// in the session.
type Attachment struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Mime string `json:"mime,omitempty"`
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
	ToolAsk     ToolView = "ask"    // questions the agent asked you
	ToolPlan    ToolView = "plan"   // a plan the agent put to you
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
	// Questions and the answers given (by question text), for an ask;
	// Plan, markdown, for a plan.
	Questions []Question        `json:"questions,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`
	Plan      string            `json:"plan,omitempty"`
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

// ChatInfo is a chat's full state.
type ChatInfo struct {
	ChatSummary
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Mode is its permission mode: default, acceptEdits, plan,
	// bypassPermissions (codex: readOnly, default, plan, bypassPermissions).
	Mode    string    `json:"mode,omitempty"`
	Created time.Time `json:"created"`
	// Pending is what it waits on you for.
	Pending *PermissionRequest `json:"pending,omitempty"`
	// Queue is the prompts waiting for its turn to end, oldest first.
	Queue []QueuedPrompt `json:"queue"`
	// Error says why its program keeps failing (State error), or what went
	// wrong last.
	Error string `json:"error,omitempty"`
}

// QueuedPrompt is a prompt the session holds until its agent can take it.
type QueuedPrompt struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	At          time.Time    `json:"at"`
}

// PermissionRequest is the agent waiting on you: to use a tool (Kind
// tool), to answer its questions (question), or to approve its plan
// (plan).
type PermissionRequest struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"` // tool | question | plan
	Tool    string    `json:"tool"`
	Summary string    `json:"summary"` // the call in one line
	At      time.Time `json:"at"`
	// Call is the tool call as the chat shows it: the command, the edit.
	Call *ToolCall `json:"call,omitempty"`
	// Reason is why the agent asks, when it says.
	Reason string `json:"reason,omitempty"`
	// AllowSession: the request can be allowed for the rest of the chat.
	AllowSession bool `json:"allowSession,omitempty"`
	// Questions are what the agent asks, under Title.
	Questions []Question `json:"questions,omitempty"`
	Title     string     `json:"title,omitempty"`
	// Plan is the plan to approve, markdown.
	Plan string `json:"plan,omitempty"`
}

// Question is one question the agent asks.
type Question struct {
	// ID keys the question's answer (the question itself, for claude).
	ID       string `json:"id"`
	Question string `json:"question"`
	Header   string `json:"header,omitempty"` // a short chip: "Auth method"
	Helper   string `json:"helper,omitempty"`
	// Kind: choice (pick from Options), text or number.
	Kind        string           `json:"kind"`
	Options     []QuestionOption `json:"options,omitempty"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
	Placeholder string           `json:"placeholder,omitempty"`
	// Number questions: the range, step, starting value and unit.
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
	Default *float64 `json:"default,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	// Other lets an answer be typed instead of picked; Secret hides it as
	// it is typed.
	Other  bool `json:"other,omitempty"`
	Secret bool `json:"secret,omitempty"`
}

// QuestionOption is one choice of a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Preview is shown while the option is focused: markdown, or HTML when
	// PreviewHTML.
	Preview     string `json:"preview,omitempty"`
	PreviewHTML bool   `json:"previewHtml,omitempty"`
}

// ChatSnapshot is a chat as a watch begins.
type ChatSnapshot struct {
	Items []ChatItem `json:"items"`
	// More says older items exist (chat.history pages them in).
	More bool      `json:"more"`
	Chat *ChatInfo `json:"chat,omitempty"`
	// Streaming are the items the agent is writing right now, as far as it
	// got: deltas add to them until the finished item replaces them.
	Streaming []ChatItem `json:"streaming,omitempty"`
	// Live is false for a session that isn't running: the items are what
	// this Mac last saw, as of AsOf.
	Live bool       `json:"live"`
	AsOf *time.Time `json:"asOf,omitempty"`
}

// ChatOp says how an update applies.
type ChatOp string

const (
	ChatAppend ChatOp = "append" // new items at the end
	ChatUpdate ChatOp = "update" // items replacing ones with the same id
	ChatReset  ChatOp = "reset"  // the conversation started over: replace everything
	ChatState     ChatOp = "chat"      // only the chat's state changed
	ChatDeltas    ChatOp = "delta"     // text the agent is writing
	ChatStreaming ChatOp = "streaming" // Items replace what is being written
)

// ChatChange is one update of a chat watch.
type ChatChange struct {
	Op    ChatOp     `json:"op"`
	Items []ChatItem `json:"items,omitempty"`
	// More goes with a reset: older items exist.
	More   bool        `json:"more,omitempty"`
	Chat   *ChatInfo   `json:"chat,omitempty"`
	Deltas []ChatDelta `json:"deltas,omitempty"`
	// Live and AsOf come with a chat change when the session's stream
	// comes or goes.
	Live *bool      `json:"live,omitempty"`
	AsOf *time.Time `json:"asOf,omitempty"`
}

// ChatDelta is text the agent wrote into a streaming item: Text goes at
// its end (the item starts, of Kind, if it is new).
type ChatDelta struct {
	ID   string   `json:"id"`
	Kind ChatKind `json:"kind"`
	Text string   `json:"text"`
}

// ChatRef names one chat.
type ChatRef struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
}

// ChatsSnapshot is a session's chats as a watch begins, oldest first.
type ChatsSnapshot struct {
	Chats []ChatInfo `json:"chats"`
	Live  bool       `json:"live"`
}

// ChatsChange is one update of a chats watch: Chats replaces the list.
type ChatsChange struct {
	Chats []ChatInfo `json:"chats"`
	Live  bool       `json:"live"`
}

// ChatCreateParams starts a chat in a session. ChatID, chosen by the
// client, makes the create happen once.
type ChatCreateParams struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
	Agent     string `json:"agent"` // claude | codex
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Title     string `json:"title,omitempty"`
	// Prompt starts it, when given.
	Prompt *ChatPrompt `json:"prompt,omitempty"`
}

// ChatPrompt is a prompt with the files that go with it.
type ChatPrompt struct {
	// ID makes the send happen once: retry with the same id.
	ID   string `json:"id"`
	Text string `json:"text"`
	// Attachments are files already in the session (files.upload put
	// them there).
	Attachments []Attachment `json:"attachments,omitempty"`
}

// ChatSendParams sends a prompt to a chat.
type ChatSendParams struct {
	SessionID string     `json:"sessionId"`
	ChatID    string     `json:"chatId"`
	Prompt    ChatPrompt `json:"prompt"`
	// Mode "steer" puts it into the turn that is running; otherwise
	// ("queue") it waits for the turn to end.
	Mode string `json:"mode,omitempty"`
}

// ChatCreated is a chat started (or, for a parked session, on its way).
type ChatCreated struct {
	ChatID string `json:"chatId"`
	// Job is set when the session was parked: a background job wakes it and
	// starts the chat.
	Job *Job `json:"job,omitempty"`
}

// DequeueParams takes back a prompt still waiting in a chat's queue.
type DequeueParams struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
	PromptID  string `json:"promptId"`
}

// ChatSendResult says how a prompt is on its way.
type ChatSendResult struct {
	// Job is set when the session was parked: a background job wakes it and
	// queues the prompt.
	Job *Job `json:"job,omitempty"`
}

// ChatConfigureParams changes a chat as it goes on: its model, effort,
// permission mode or title. Empty fields stay as they are.
type ChatConfigureParams struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Title     string `json:"title,omitempty"`
}

// DecideParams answers what a chat waits on: allow or deny a tool use
// (allowSession: for the rest of the chat); answer its questions (allow
// with Answers); approve its plan (allow, Mode to switch to) or send it
// back (deny with Message, the feedback).
type DecideParams struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
	RequestID string `json:"requestId"`
	Behavior  string `json:"behavior"` // allow | allowSession | deny
	Message   string `json:"message,omitempty"`
	// Answers by question id; several choices joined with ", ".
	Answers map[string]string `json:"answers,omitempty"`
	// Notes the user added to an answer, by question id.
	Notes map[string]string `json:"notes,omitempty"`
	Mode  string            `json:"mode,omitempty"`
}

// WindowRef names a tmux window of a session: a terminal tab opens on it.
type WindowRef struct {
	WindowID string `json:"windowId"`
}

// ModelsParams asks what an agent offers.
type ModelsParams struct {
	Agent string `json:"agent"` // claude | codex
	// SessionID, when set, asks that session's agent; otherwise the last
	// answer this Mac saw.
	SessionID string `json:"sessionId,omitempty"`
}

// AgentModels is what an agent offers: models, efforts, permission modes.
type AgentModels struct {
	Agent  string      `json:"agent"`
	Models []ModelInfo `json:"models"`
	Modes  []ModeInfo  `json:"modes"`
}

// ModelInfo is one model an agent can run.
type ModelInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
	// DefaultEffort is the effort it runs at unless told otherwise.
	DefaultEffort string `json:"defaultEffort,omitempty"`
	Default       bool   `json:"default,omitempty"`
}

// ModeInfo is one permission mode.
type ModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// HistoryParams pages older chat items in.
type HistoryParams struct {
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId"`
	Before    string `json:"before"` // the oldest item id the client has
	Limit     int    `json:"limit,omitempty"`
}

// HistoryPage is a page of older items, oldest first.
type HistoryPage struct {
	Items []ChatItem `json:"items"`
	More  bool       `json:"more"`
}

// UploadParams puts a file attached to a prompt into a session. A parked
// session's files wait on this Mac (the attachment's path starts with
// "staged:") until the prompt wakes it.
type UploadParams struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Mime      string `json:"mime,omitempty"`
	Data      string `json:"data"` // base64
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
	Kind      string `json:"kind"` // finished | needs_input | setup_failed | create_failed | ci_failed
	SessionID string `json:"sessionId"`
	ChatID    string `json:"chatId,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

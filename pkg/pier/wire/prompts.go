package wire

// PromptsParams names whose prompts: a repo's, or ("") every repo's.
type PromptsParams struct {
	Repo string `json:"repo,omitempty"`
}

// Prompt is one prompt pier sends agents, as settings edit it.
type Prompt struct {
	Key         string `json:"key"` // fixCI | addressComments | createPR | onboard | review
	Title       string `json:"title"`
	Description string `json:"description"`
	// Own is this level's text ("" for none); Inherited what it falls back
	// to; Effective what is sent. {pr} is the pull request's number.
	Own       string `json:"own"`
	Inherited string `json:"inherited"`
	Effective string `json:"effective"`
}

// Prompts are a level's prompts.
type Prompts struct {
	Prompts []Prompt `json:"prompts"`
}

// PromptSetParams sets a prompt; empty text falls back again.
type PromptSetParams struct {
	Repo string `json:"repo,omitempty"`
	Key  string `json:"key"`
	Text string `json:"text"`
}

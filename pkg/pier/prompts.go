package pier

import "github.com/usepier/pier/internal/config"

// Prompt is one of the prompts pier sends agents, as a settings page edits
// it: for a repo, or for every repo (Repo "").
type Prompt struct {
	Key, Title, Description string
	// Own is this level's text ("" when it has none); Inherited what it
	// falls back to (the user's for a repo, else pier's); Effective what is
	// sent.
	Own, Inherited, Effective string
}

// Prompts are the prompts for a repo ("" for every repo).
func (c *Client) Prompts(repo string) []Prompt {
	out := make([]Prompt, 0, len(config.PromptKinds))
	for _, k := range config.PromptKinds {
		p := Prompt{Key: k.Key, Title: k.Title, Description: k.Description, Inherited: k.Default}
		if repo != "" {
			p.Inherited = c.conf().Prompt("", k.Key)
		}
		p.Own = c.conf().PromptOverride(repo, k.Key)
		p.Effective = c.conf().Prompt(repo, k.Key)
		out = append(out, p)
	}
	return out
}

// SetPrompt sets a prompt for a repo ("" for every repo); empty text falls
// back again.
func (c *Client) SetPrompt(repo, key, text string) error {
	next, err := config.Update(func(cfg *config.Config) error {
		return cfg.SetPrompt(repo, key, text)
	})
	if err != nil {
		return err
	}
	c.setConf(next)
	return nil
}

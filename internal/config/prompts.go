package config

import (
	"fmt"
	"strings"
)

// Prompts are what pier asks an agent to do for the user — fix CI, address
// review comments, open a pull request, onboard a repo, and the line that
// heads review comments. Empty keeps pier's wording; Repos override them
// for one repo (by basename).
type Prompts struct {
	PromptSet
	Repos map[string]PromptSet `toml:"repos,omitempty"`
}

// PromptSet is one set of prompts; empty ones fall through.
type PromptSet struct {
	FixCI           string `toml:"fix_ci,omitempty"`
	AddressComments string `toml:"address_comments,omitempty"`
	CreatePR        string `toml:"create_pr,omitempty"`
	Onboard         string `toml:"onboard,omitempty"`
	Review          string `toml:"review,omitempty"`
}

// PromptKind describes one prompt.
type PromptKind struct {
	Key         string // fixCI, addressComments, createPR, onboard, review
	Title       string
	Description string
	Default     string
}

// PromptKinds are the prompts, in the order a settings page shows them.
// {pr} stands for the pull request's number.
var PromptKinds = []PromptKind{
	{"fixCI", "Fix CI", "Starts the message that sends the failing checks' logs to the agent.",
		"CI is failing on PR #{pr}. Find out why, fix it, and push."},
	{"addressComments", "Address review comments", "Starts the message that sends the pull request's open review feedback to the agent.",
		"Address the review feedback on PR #{pr}, then push. Reply to nothing — just make the changes."},
	{"createPR", "Create a pull request", "What Create PR asks the agent.",
		"Open a pull request for this branch: commit anything outstanding, push, then create it with `gh pr create` — a clear title, and a body that sums up what changed and why."},
	{"onboard", "Set a repo up for pier", "The first message of the session that onboards a repository.",
		"Set this repository up for pier, using the pier-onboard skill: write .pier/setup.sh so a fresh session installs dependencies and starts services, check it runs, and open a pull request with it."},
	{"review", "Review comments", "Heads your diff and file comments when they go to the agent.",
		"Review comments on your changes — please address each one:"},
}

func (s *PromptSet) field(key string) (*string, error) {
	switch key {
	case "fixCI":
		return &s.FixCI, nil
	case "addressComments":
		return &s.AddressComments, nil
	case "createPR":
		return &s.CreatePR, nil
	case "onboard":
		return &s.Onboard, nil
	case "review":
		return &s.Review, nil
	}
	return nil, fmt.Errorf("unknown prompt %q", key)
}

func (s PromptSet) get(key string) string {
	f, err := s.field(key)
	if err != nil {
		return ""
	}
	return *f
}

func defaultPrompt(key string) string {
	for _, k := range PromptKinds {
		if k.Key == key {
			return k.Default
		}
	}
	return ""
}

// Prompt is the prompt of a kind for a repo ("" for none): the repo's own,
// else the user's, else pier's.
func (c Config) Prompt(repo, key string) string {
	if p := c.Prompts.Repos[repo].get(key); strings.TrimSpace(p) != "" {
		return p
	}
	if p := c.Prompts.get(key); strings.TrimSpace(p) != "" {
		return p
	}
	return defaultPrompt(key)
}

// SetPrompt sets a prompt for a repo ("" for every repo); empty text goes
// back to what it falls through to.
func (c *Config) SetPrompt(repo, key, text string) error {
	text = strings.TrimSpace(text)
	if repo == "" {
		f, err := c.Prompts.field(key)
		if err != nil {
			return err
		}
		*f = text
		return nil
	}
	if strings.ContainsAny(repo, "/\\") || repo == "." || repo == ".." {
		return fmt.Errorf("bad repo %q", repo)
	}
	set := c.Prompts.Repos[repo]
	f, err := set.field(key)
	if err != nil {
		return err
	}
	*f = text
	if c.Prompts.Repos == nil {
		c.Prompts.Repos = map[string]PromptSet{}
	}
	if set == (PromptSet{}) {
		delete(c.Prompts.Repos, repo)
	} else {
		c.Prompts.Repos[repo] = set
	}
	return nil
}

// PromptOverride is a repo's or the user's own text for a prompt ("" for
// none).
func (c Config) PromptOverride(repo, key string) string {
	if repo != "" {
		return c.Prompts.Repos[repo].get(key)
	}
	return c.Prompts.get(key)
}

// FillPrompt puts a pull request's number into a prompt.
func FillPrompt(p string, pr int) string {
	return strings.ReplaceAll(p, "{pr}", fmt.Sprint(pr))
}

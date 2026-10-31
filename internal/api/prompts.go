package api

import (
	"context"

	"github.com/usepier/pier/pkg/pier/wire"
)

func init() {
	handle("prompts.get", "The prompts pier sends agents (fix CI, address comments, create a PR, onboard, review comments) for a repo, or every repo: each with its own text, what it falls back to, and what is sent.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PromptsParams) (wire.Prompts, error) {
			be, err := s.backend()
			if err != nil {
				return wire.Prompts{}, err
			}
			out := wire.Prompts{Prompts: []wire.Prompt{}}
			for _, x := range be.Prompts(p.Repo) {
				out.Prompts = append(out.Prompts, wire.Prompt{Key: x.Key, Title: x.Title, Description: x.Description,
					Own: x.Own, Inherited: x.Inherited, Effective: x.Effective})
			}
			return out, nil
		})
	handle("prompts.set", "Sets a prompt for a repo, or every repo; empty text falls back again.",
		func(ctx context.Context, s *Server, _ *Call, p wire.PromptSetParams) (*wire.Empty, error) {
			be, err := s.backend()
			if err != nil {
				return nil, err
			}
			return nil, be.SetPrompt(p.Repo, p.Key, p.Text)
		})
}

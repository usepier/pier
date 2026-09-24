// Package skills ships the agent skills bundled with pier, embedded so the
// installed binary can lay them down (`pier setup`) without a repo checkout
// — brew users never see this directory.
package skills

import "embed"

// FS holds every bundled skill, one directory per skill (<name>/SKILL.md
// plus any reference files) in the Agent Skills layout that Claude Code and
// Codex both read.
//
//go:embed pier-onboard
var FS embed.FS

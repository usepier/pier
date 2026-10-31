package main

// A session made with a stand-in branch name and a first prompt names its
// branch after that prompt: the session's agent CLI is asked for a short
// name, and the branch is renamed — as long as nothing was pushed from it
// and the agent hasn't moved to another branch meanwhile. The status says
// what it became ("renamed"); the laptop relabels the session after it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// branchesEvery is how often the status reads the session's branches.
const branchesEvery = 15 * time.Second

// branchName records that the naming ran: once per session.
type branchName struct {
	From string `json:"from"`
	To   string `json:"to,omitempty"` // empty when it gave up: the stand-in stays
}

func branchNamePath(home string) string { return filepath.Join(home, ".pier", "branch-name.json") }

// sessionBranches is what the laptop should know the session's branch by:
// renamed, the branch it named itself, while the checkout is still on it;
// own, the checkout's branch once it's on origin under its name and isn't
// the branch the session came off — an agent may start a branch of its own
// and push it, and the session's pull request is that branch's. Each is ""
// when it doesn't hold: what the laptop has stands.
func sessionBranches(home string) (renamed, own string) {
	dir := checkoutDir(home)
	cur, err := gitOut(dir, 5*time.Second, "symbolic-ref", "--short", "-q", "HEAD")
	if cur = strings.TrimSpace(cur); err != nil || cur == "" {
		return "", "" // detached, or no checkout yet
	}
	if r := renamedBranch(home); r == cur {
		renamed = r
	}
	if b, _ := gitOut(dir, 5*time.Second, "config", "pier.base"); strings.TrimSpace(b) == cur {
		return renamed, ""
	}
	if up, err := gitOut(dir, 5*time.Second, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && strings.TrimSpace(up) == "origin/"+cur {
		own = cur
	}
	return renamed, own
}

// renamedBranch is the branch this session named itself, if it did.
func renamedBranch(home string) string {
	b, err := os.ReadFile(branchNamePath(home))
	if err != nil {
		return ""
	}
	var n branchName
	if json.Unmarshal(b, &n) != nil {
		return ""
	}
	return n.To
}

// nameBranch names the branch after prompt, in the background, unless it
// already ran (or is running).
func (h *chatHost) nameBranch(agent, standIn, prompt string) {
	h.mu.Lock()
	running := h.naming
	h.naming = true
	h.mu.Unlock()
	if running {
		return
	}
	if _, err := os.Stat(branchNamePath(h.home)); err == nil {
		return
	}
	go func() {
		rec := branchName{From: standIn}
		name, err := h.suggestBranch(agent, prompt)
		if err == nil {
			rec.To, err = renameBranch(checkoutDir(h.home), standIn, name)
		}
		if err != nil {
			fmt.Println("pier-supervisor: naming the branch:", err)
		}
		b, _ := json.Marshal(rec)
		_ = os.MkdirAll(filepath.Dir(branchNamePath(h.home)), 0o700)
		_ = os.WriteFile(branchNamePath(h.home), b, 0o600)
	}()
}

const branchAsk = "Name a git branch for the task below. Answer with the name only: two to five lowercase words joined by hyphens — no prefix, no quotes, nothing else.\n\nThe task:\n"

// suggestBranch asks the agent's CLI for a branch name: its small, quick
// model where it has one.
func (h *chatHost) suggestBranch(agent, prompt string) (string, error) {
	if r := []rune(prompt); len(r) > 4000 {
		prompt = string(r[:4000])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tmp, err := os.MkdirTemp("", "pier-name-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	var cmd *exec.Cmd
	answer := filepath.Join(tmp, "answer")
	switch agent {
	case "codex":
		bin, note := h.program("codex")
		if bin == "" {
			return "", errors.New(note)
		}
		cmd = exec.CommandContext(ctx, bin, "exec", "--skip-git-repo-check", "--sandbox", "read-only",
			"-c", `model_reasoning_effort="low"`, "--output-last-message", answer, branchAsk+prompt)
	default:
		bin, note := h.program("claude")
		if bin == "" {
			return "", errors.New(note)
		}
		cmd = exec.CommandContext(ctx, bin, "-p", "--model", "haiku", "--output-format", "text", branchAsk+prompt)
	}
	// Outside the checkout: the name needs the prompt, not the repo.
	cmd.Dir = tmp
	cmd.Env = agentEnv(h.home)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", agent, err)
	}
	if b, err := os.ReadFile(answer); err == nil {
		out = b
	}
	name := branchSlug(string(out))
	if name == "" {
		return "", fmt.Errorf("%s answered %q, not a branch name", agent, strings.TrimSpace(string(out)))
	}
	return name, nil
}

var (
	slugJunk    = regexp.MustCompile(`[^a-z0-9/]+`)
	slugSlashes = regexp.MustCompile(`/{2,}`)
)

// branchSlug makes an answer a branch name: its last line, lowercase words
// joined by hyphens, at most 48 characters, cut at a word.
func branchSlug(answer string) string {
	lines := strings.Split(strings.TrimSpace(answer), "\n")
	s := strings.ToLower(strings.TrimSpace(lines[len(lines)-1]))
	s = strings.Trim(slugJunk.ReplaceAllString(s, "-"), "-/")
	s = slugSlashes.ReplaceAllString(s, "/")
	for len(s) > 48 {
		i := strings.LastIndexAny(s[:48], "-/")
		if i <= 0 {
			s = s[:48]
			break
		}
		s = s[:i]
	}
	return strings.Trim(s, "-/")
}

// renameBranch renames the checkout's branch from standIn to name (or
// name-2, … when that's taken here or on origin), and returns what it
// became. It refuses once the branch was pushed or the checkout moved to
// another branch: a name others may have seen stays.
func renameBranch(dir, standIn, name string) (string, error) {
	cur, err := gitOut(dir, 10*time.Second, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	if cur = strings.TrimSpace(cur); cur != standIn {
		return "", fmt.Errorf("the checkout is on %s now, not %s", cur, standIn)
	}
	if _, err := gitOut(dir, 10*time.Second, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		return "", fmt.Errorf("%s was pushed already", standIn)
	}
	if name == standIn {
		return name, nil
	}
	to := name
	for n := 2; branchTaken(dir, to); n++ {
		to = fmt.Sprintf("%s-%d", name, n)
	}
	if _, err := gitOut(dir, 10*time.Second, "branch", "-m", standIn, to); err != nil {
		return "", err
	}
	return to, nil
}

// branchTaken: a branch of that name is here, or on origin.
func branchTaken(dir, name string) bool {
	if _, err := gitOut(dir, 10*time.Second, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err == nil {
		return true
	}
	out, _ := gitOut(dir, 20*time.Second, "ls-remote", "--heads", "origin", name)
	return strings.TrimSpace(out) != ""
}

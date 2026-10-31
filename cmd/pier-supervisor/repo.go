package main

// The session's checkout, read here: what the branch changed against its
// base (pushed to frontends as it changes), a file's diff, its contents at
// the base and in the working tree, the file list, and searches streamed
// as they find. Frontends ask by name; nothing is a shell script, and the
// checkout is wherever the session has it.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/usepier/pier/pkg/pier/gitview"
)

// codedError is an op failure frontends tell apart by its code.
type codedError struct {
	code string
	msg  string
}

func (e *codedError) Error() string { return e.msg }

// Codes of op failures.
const (
	codeNoCheckout = "no_checkout" // the session has no checkout (yet)
	codeConflict   = "conflict"    // the file changed since it was read
	codeOutside    = "outside"     // the path leaves the checkout
	codeGit        = "git"         // git refused
)

func coded(code, format string, a ...any) error {
	return &codedError{code: code, msg: fmt.Sprintf(format, a...)}
}

// errCode is an error's code, "" for an uncoded one.
func errCode(err error) string {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	return ""
}

// repoRoot is the session's checkout: the git repository under ~/work.
func repoRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := checkoutDir(home)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", coded(codeNoCheckout, "the session has no checkout yet")
	}
	return dir, nil
}

// gitOut runs git in dir and returns its output.
func gitOut(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Reading never takes git's index lock from under the agent.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			msg = "timed out"
		}
		return string(out), coded(codeGit, "git %s: %s", args[0], msg)
	}
	return string(out), nil
}

// base is what the branch is compared with: its merge base with the
// origin branch it came off, else the commit pier recorded at create.
func base(dir string) (ref, oid string) {
	if b, _ := gitOut(dir, 5*time.Second, "config", "pier.base"); strings.TrimSpace(b) != "" {
		r := "origin/" + strings.TrimSpace(b)
		if _, err := gitOut(dir, 5*time.Second, "rev-parse", "-q", "--verify", "refs/remotes/"+r); err == nil {
			ref = r
			if mb, err := gitOut(dir, 10*time.Second, "merge-base", "HEAD", r); err == nil {
				oid = strings.TrimSpace(mb)
			}
		}
	}
	for _, rev := range []string{"refs/pier/base", "HEAD"} {
		if oid != "" {
			break
		}
		if o, err := gitOut(dir, 5*time.Second, "rev-parse", "-q", "--verify", rev); err == nil {
			oid = strings.TrimSpace(o)
		}
	}
	return ref, oid
}

// lineCount counts a file's lines, for untracked files' added lines; -1
// for one too large or binary.
func lineCount(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return -1
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() > 4<<20 {
		return -1
	}
	b, err := io.ReadAll(f)
	if err != nil || bytes.IndexByte(b, 0) >= 0 {
		return -1
	}
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// readChanges reads how the checkout differs from its base.
func readChanges(dir string) (gitview.Changes, error) {
	ref, oid := base(dir)
	status, err := gitOut(dir, 30*time.Second, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return gitview.Changes{}, err
	}
	var nameStatus, numstat string
	if oid != "" {
		if nameStatus, err = gitOut(dir, 30*time.Second, "diff", "--name-status", "-z", "-M", oid); err != nil {
			return gitview.Changes{}, err
		}
		if numstat, err = gitOut(dir, 30*time.Second, "diff", "--numstat", "-z", "-M", oid); err != nil {
			return gitview.Changes{}, err
		}
	}
	var untracked strings.Builder
	if out, err := gitOut(dir, 30*time.Second, "ls-files", "-o", "--exclude-standard", "-z"); err == nil {
		for i, p := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
			if p == "" || i >= 400 {
				continue
			}
			if n := lineCount(filepath.Join(dir, p)); n >= 0 {
				fmt.Fprintf(&untracked, "%d %s\n", n, p)
			}
		}
	}
	return gitview.Parse(gitview.Snapshot{BaseRef: ref, BaseOid: oid, Status: status, NameStatus: nameStatus, Numstat: numstat, UntrackedLines: untracked.String()}), nil
}

// maxPatch bounds a diff or a file read; maxPatches a batch of diffs.
const (
	maxPatch   = 2 << 20
	maxPatches = 6 << 20
)

type diffArgs struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath"`
	Status  string `json:"status"`
	// Context is the lines around each change (0 for git's default).
	Context int `json:"context"`
}

// diff is one file's patch against the base; an untracked file diffs
// against nothing.
func diff(dir, oid string, a diffArgs) (patch string, truncated bool, err error) {
	if _, err := inside(dir, a.Path); err != nil {
		return "", false, coded(codeOutside, "%v", err)
	}
	args := []string{"diff", "-M"}
	if a.Context > 0 {
		args = append(args, "-U"+strconv.Itoa(a.Context))
	}
	if a.Status == string(gitview.Untracked) {
		args = append(args, "--no-index", "--", "/dev/null", a.Path)
	} else {
		args = append(args, oid, "--")
		if a.OldPath != "" {
			args = append(args, a.OldPath)
		}
		args = append(args, a.Path)
	}
	out, err := gitOut(dir, 30*time.Second, args...)
	// diff --no-index exits 1 when the files differ: that is its answer.
	if err != nil && out == "" {
		return "", false, err
	}
	if len(out) > maxPatch {
		out, truncated = out[:maxPatch], true
	}
	return out, truncated, nil
}

// fileAt is a file's content at the base ("base") or in the working tree.
type fileContent struct {
	Data     string `json:"data,omitempty"` // base64
	Size     int64  `json:"size"`
	Sha      string `json:"sha,omitempty"`
	Missing  bool   `json:"missing,omitempty"`
	TooLarge bool   `json:"tooLarge,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
}

func readAt(dir, oid, path, at string) (fileContent, error) {
	p, err := inside(dir, path)
	if err != nil {
		return fileContent{}, coded(codeOutside, "%v", err)
	}
	var data []byte
	switch at {
	case "base":
		if oid == "" {
			return fileContent{Missing: true}, nil
		}
		out, err := gitOut(dir, 30*time.Second, "show", oid+":"+filepath.ToSlash(path))
		if err != nil {
			return fileContent{Missing: true}, nil
		}
		data = []byte(out)
	default:
		fi, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			return fileContent{Missing: true}, nil
		}
		if err != nil {
			return fileContent{}, err
		}
		if fi.IsDir() {
			return fileContent{}, fmt.Errorf("%s is a directory", path)
		}
		f, err := os.Open(p)
		if err != nil {
			return fileContent{}, err
		}
		data, err = io.ReadAll(io.LimitReader(f, maxPatch+1))
		f.Close()
		if err != nil {
			return fileContent{}, err
		}
	}
	fc := fileContent{Size: int64(len(data))}
	if len(data) > maxPatch {
		data, fc.TooLarge = data[:maxPatch], true
	} else {
		sum := sha256.Sum256(data)
		fc.Sha = hex.EncodeToString(sum[:])
	}
	fc.Binary = bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
	fc.Data = base64.StdEncoding.EncodeToString(data)
	return fc, nil
}

// maxListed bounds the file list.
const maxListed = 50000

func listFiles(dir string) ([]string, bool, error) {
	out, err := gitOut(dir, 30*time.Second, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] {
			continue
		}
		if len(paths) == maxListed {
			return paths, true, nil
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, false, nil
}

func init() {
	queries["repo.changes"] = func(json.RawMessage) (any, error) {
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		return readChanges(dir)
	}
	queries["repo.diff"] = func(raw json.RawMessage) (any, error) {
		var a diffArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		_, oid := base(dir)
		patch, truncated, err := diff(dir, oid, a)
		return map[string]any{"patch": patch, "truncated": truncated}, err
	}
	queries["repo.diffs"] = func(raw json.RawMessage) (any, error) {
		var a struct {
			Files []diffArgs `json:"files"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		_, oid := base(dir)
		patches := map[string]string{}
		total, skipped := 0, 0
		for _, f := range a.Files {
			p, _, err := diff(dir, oid, f)
			if err != nil || total+len(p) > maxPatches {
				skipped++
				continue
			}
			total += len(p)
			patches[f.Path] = p
		}
		return map[string]any{"patches": patches, "skipped": skipped}, nil
	}
	queries["repo.file"] = func(raw json.RawMessage) (any, error) {
		var a struct{ Path, At string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		oid := ""
		if a.At == "base" {
			_, oid = base(dir)
		}
		return readAt(dir, oid, a.Path, a.At)
	}
	queries["repo.files"] = func(raw json.RawMessage) (any, error) {
		// Several files at once, up to maxPatches in all: what a frontend
		// keeps to show while the session is parked.
		var a struct {
			Paths []string `json:"paths"`
			At    string   `json:"at"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		oid := ""
		if a.At == "base" {
			_, oid = base(dir)
		}
		files := map[string]fileContent{}
		total, skipped := 0, 0
		for _, p := range a.Paths {
			fc, err := readAt(dir, oid, p, a.At)
			if err != nil || total+len(fc.Data) > maxPatches {
				skipped++
				continue
			}
			total += len(fc.Data)
			files[p] = fc
		}
		return map[string]any{"files": files, "skipped": skipped}, nil
	}
	queries["repo.list"] = func(json.RawMessage) (any, error) {
		dir, err := repoRoot()
		if err != nil {
			return nil, err
		}
		paths, truncated, err := listFiles(dir)
		return map[string]any{"paths": paths, "truncated": truncated}, err
	}
}

// --- search ------------------------------------------------------------------

type searchArgs struct {
	ID            string `json:"id"`
	Query         string `json:"query"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
}

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// maxMatches bounds a search.
const maxMatches = 5000

// searchMessage is a batch of a search's matches for its frontend.
func searchMessage(id string, ms []searchMatch, done, truncated bool, err error) message {
	if ms == nil {
		ms = []searchMatch{}
	}
	m := message{Ev: "search", ID: id, Data: mustJSON(map[string]any{"matches": ms, "done": done, "truncated": truncated})}
	if err != nil {
		m.Err, m.Code = err.Error(), errCode(err)
	}
	return m
}

// search runs a search with ripgrep, sending matches as they come (every
// 100 ms at most) until done, cancelled or at maxMatches.
func (h *hub) search(ctx context.Context, a searchArgs, send func(ms []searchMatch, done, truncated bool, err error)) {
	dir, err := repoRoot()
	if err != nil {
		send(nil, true, false, err)
		return
	}
	if strings.TrimSpace(a.Query) == "" {
		send(nil, true, false, nil)
		return
	}
	var rg, note string
	if h.host != nil {
		rg, note = h.host.pins.program("rg")
	} else if p, err := exec.LookPath("rg"); err == nil {
		rg = p
	}
	if rg == "" {
		send(nil, true, false, fmt.Errorf("no ripgrep: %s", note))
		return
	}
	args := []string{"--json", "--hidden", "--glob", "!.git", "--max-columns", "400", "--max-filesize", "4M"}
	if !a.Regex {
		args = append(args, "--fixed-strings")
	}
	if !a.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	args = append(args, "--", a.Query, ".")
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		send(nil, true, false, err)
		return
	}
	if err := cmd.Start(); err != nil {
		send(nil, true, false, err)
		return
	}
	var batch []searchMatch
	last := time.Now()
	n := 0
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
				Submatches []struct {
					Start int `json:"start"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "match" {
			continue
		}
		col := 1
		if len(ev.Data.Submatches) > 0 {
			col = utf8.RuneCountInString(ev.Data.Lines.Text[:min(ev.Data.Submatches[0].Start, len(ev.Data.Lines.Text))]) + 1
		}
		text := strings.TrimRight(ev.Data.Lines.Text, "\r\n")
		if len(text) > 400 {
			text = text[:400]
		}
		batch = append(batch, searchMatch{Path: strings.TrimPrefix(ev.Data.Path.Text, "./"), Line: ev.Data.LineNumber, Col: col, Text: text})
		n++
		if n >= maxMatches {
			cmd.Process.Kill()
			cmd.Wait()
			send(batch, true, true, nil)
			return
		}
		if time.Since(last) > 100*time.Millisecond {
			send(batch, false, false, nil)
			batch, last = nil, time.Now()
		}
	}
	// ripgrep exits 1 for no match, 2 for an error (a bad pattern, say).
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) && ee.ExitCode() == 2 && n == 0 {
		send(nil, true, false, errors.New(firstLine(stderr.String())))
		return
	}
	send(batch, true, false, nil)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

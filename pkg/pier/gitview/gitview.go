// Package gitview reads a session checkout's state from git's machine
// formats — status --porcelain=v2 -z, diff --name-status -z and --numstat
// -z — into what a review shows: every file that differs from where the
// branch started, with its line counts, and which of them aren't committed.
package gitview

import (
	"sort"
	"strconv"
	"strings"
)

// Status is how a file differs from the base.
type Status string

const (
	Added      Status = "added"
	Modified   Status = "modified"
	Deleted    Status = "deleted"
	Renamed    Status = "renamed"
	Untracked  Status = "untracked"
	Conflicted Status = "conflicted"
)

// File is one changed file.
type File struct {
	Path    string
	OldPath string // renames
	Status  Status
	Added   int
	Removed int
	Binary  bool
	// Uncommitted: the working tree or index differs from HEAD here.
	Uncommitted bool
}

// Branch is what status --branch says.
type Branch struct {
	Head     string // branch name; "" when detached
	Oid      string
	Upstream string
	Ahead    int
	Behind   int
}

// Changes is a checkout against its base.
type Changes struct {
	Branch Branch
	// BaseRef is what the branch is compared with (origin/main); BaseOid
	// the commit the branch started from (their merge base).
	BaseRef string
	BaseOid string
	Files   []File
}

// Uncommitted counts the files with uncommitted changes.
func (c Changes) Uncommitted() int {
	n := 0
	for _, f := range c.Files {
		if f.Uncommitted {
			n++
		}
	}
	return n
}

// Snapshot is the raw output a snapshot script gathers, section by
// section.
type Snapshot struct {
	BaseRef, BaseOid string
	Status           string // status --porcelain=v2 -z --branch --untracked-files=all
	NameStatus       string // diff --name-status -z <base>
	Numstat          string // diff --numstat -z <base>
	UntrackedLines   string // wc -l of untracked files, one "count path" per line
}

// Parse builds the review from a snapshot.
func Parse(s Snapshot) Changes {
	c := Changes{BaseRef: s.BaseRef, BaseOid: s.BaseOid}
	files := map[string]*File{}
	get := func(path string) *File {
		f := files[path]
		if f == nil {
			f = &File{Path: path}
			files[path] = f
		}
		return f
	}

	// What differs from the base, committed or not.
	ns := splitZ(s.NameStatus)
	for i := 0; i < len(ns); i++ {
		code := ns[i]
		if code == "" {
			continue
		}
		switch code[0] {
		case 'R', 'C':
			if i+2 >= len(ns) {
				break
			}
			f := get(ns[i+2])
			f.OldPath, f.Status = ns[i+1], Renamed
			if code[0] == 'C' {
				f.OldPath, f.Status = "", Added
			}
			i += 2
		default:
			if i+1 >= len(ns) {
				break
			}
			f := get(ns[i+1])
			f.Status = map[byte]Status{'A': Added, 'D': Deleted, 'U': Conflicted}[code[0]]
			if f.Status == "" {
				f.Status = Modified
			}
			i++
		}
	}
	nm := splitZ(s.Numstat)
	for i := 0; i < len(nm); i++ {
		parts := strings.SplitN(nm[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(nm) { // a rename: old and new follow
			path = nm[i+2]
			i += 2
		}
		f := get(path)
		if parts[0] == "-" {
			f.Binary = true
			continue
		}
		f.Added, _ = strconv.Atoi(parts[0])
		f.Removed, _ = strconv.Atoi(parts[1])
	}

	// What isn't committed, and the branch.
	st := splitZ(s.Status)
	for i := 0; i < len(st); i++ {
		e := st[i]
		switch {
		case strings.HasPrefix(e, "# "):
			parseBranchLine(&c.Branch, e)
		case strings.HasPrefix(e, "1 "):
			if p := field(e, 8); p != "" {
				f := get(p)
				f.Uncommitted = true
				if f.Status == "" {
					f.Status = Modified
				}
			}
		case strings.HasPrefix(e, "2 "):
			if p := field(e, 9); p != "" {
				f := get(p)
				f.Uncommitted = true
				if i+1 < len(st) {
					f.OldPath, f.Status = st[i+1], Renamed
				}
			}
			i++ // the original path follows
		case strings.HasPrefix(e, "u "):
			if p := field(e, 10); p != "" {
				f := get(p)
				f.Uncommitted, f.Status = true, Conflicted
			}
		case strings.HasPrefix(e, "? "):
			f := get(e[2:])
			f.Uncommitted, f.Status = true, Untracked
		}
	}
	for _, line := range strings.Split(s.UntrackedLines, "\n") {
		line = strings.TrimSpace(line)
		n, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if f := files[strings.TrimSpace(path)]; f != nil && f.Status == Untracked {
			f.Added, _ = strconv.Atoi(n)
		}
	}

	for _, f := range files {
		if f.Status == "" {
			f.Status = Modified
		}
		c.Files = append(c.Files, *f)
	}
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Path < c.Files[j].Path })
	return c
}

func splitZ(s string) []string {
	s = strings.TrimSuffix(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// field returns the space-separated field n (0-based) and everything after
// it: the path, which may itself hold spaces.
func field(e string, n int) string {
	parts := strings.SplitN(e, " ", n+1)
	if len(parts) <= n {
		return ""
	}
	return parts[n]
}

func parseBranchLine(b *Branch, e string) {
	k, v, _ := strings.Cut(strings.TrimPrefix(e, "# "), " ")
	switch k {
	case "branch.oid":
		b.Oid = v
	case "branch.head":
		if v != "(detached)" {
			b.Head = v
		}
	case "branch.upstream":
		b.Upstream = v
	case "branch.ab":
		a, r, _ := strings.Cut(v, " ")
		b.Ahead, _ = strconv.Atoi(strings.TrimPrefix(a, "+"))
		b.Behind, _ = strconv.Atoi(strings.TrimPrefix(r, "-"))
	}
}

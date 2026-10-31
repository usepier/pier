package gitview

import (
	"strings"
	"testing"
)

func z(parts ...string) string { return strings.Join(parts, "\x00") + "\x00" }

func TestParse(t *testing.T) {
	c := Parse(Snapshot{
		BaseRef: "origin/main",
		BaseOid: "b4se",
		Status: z(
			"# branch.oid 1234",
			"# branch.head fix-login",
			"# branch.upstream origin/fix-login",
			"# branch.ab +2 -1",
			"1 .M N... 100644 100644 100644 aaa bbb src/app.go",
			"2 R. N... 100644 100644 100644 ccc ccc R100 docs/new name.md", "docs/old.md",
			"u UU N... 100644 100644 100644 100644 d1 d2 d3 go.sum",
			"? notes/todo.txt",
		),
		NameStatus:     z("M", "src/app.go", "A", "src/new.go", "D", "gone.txt", "R100", "docs/old.md", "docs/new name.md", "M", "logo.png", "U", "go.sum"),
		Numstat:        z("3\t1\tsrc/app.go", "10\t0\tsrc/new.go", "0\t4\tgone.txt", "0\t0\t", "docs/old.md", "docs/new name.md", "-\t-\tlogo.png", "1\t1\tgo.sum"),
		UntrackedLines: "  7 notes/todo.txt\n  7 total\n",
	})
	if c.Branch.Head != "fix-login" || c.Branch.Ahead != 2 || c.Branch.Behind != 1 || c.Branch.Upstream != "origin/fix-login" {
		t.Errorf("branch = %+v", c.Branch)
	}
	by := map[string]File{}
	for _, f := range c.Files {
		by[f.Path] = f
	}
	want := map[string]File{
		"src/app.go":       {Path: "src/app.go", Status: Modified, Added: 3, Removed: 1, Uncommitted: true},
		"src/new.go":       {Path: "src/new.go", Status: Added, Added: 10},
		"gone.txt":         {Path: "gone.txt", Status: Deleted, Removed: 4},
		"docs/new name.md": {Path: "docs/new name.md", OldPath: "docs/old.md", Status: Renamed, Uncommitted: true},
		"logo.png":         {Path: "logo.png", Status: Modified, Binary: true},
		"go.sum":           {Path: "go.sum", Status: Conflicted, Added: 1, Removed: 1, Uncommitted: true},
		"notes/todo.txt":   {Path: "notes/todo.txt", Status: Untracked, Added: 7, Uncommitted: true},
	}
	if len(c.Files) != len(want) {
		t.Fatalf("files = %+v", c.Files)
	}
	for p, w := range want {
		if by[p] != w {
			t.Errorf("%s = %+v, want %+v", p, by[p], w)
		}
	}
	if c.Uncommitted() != 4 {
		t.Errorf("uncommitted = %d", c.Uncommitted())
	}
}

func TestParseClean(t *testing.T) {
	c := Parse(Snapshot{Status: z("# branch.oid 1", "# branch.head (detached)")})
	if len(c.Files) != 0 || c.Branch.Head != "" {
		t.Errorf("clean = %+v", c)
	}
}

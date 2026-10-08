package pier

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m, err := OpenMirror("i-1")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(ch TranscriptChunk) {
		t.Helper()
		if err := m.Apply(ch); err != nil {
			t.Fatal(err)
		}
	}
	apply(TranscriptChunk{File: "/t/a.jsonl", Kind: "claude", Off: 0, Next: 4, Data: "{a}\n", Reset: true})
	apply(TranscriptChunk{File: "/t/a.jsonl", Kind: "claude", Off: 4, Next: 9000, Data: "{b}\n", Skipped: 1})
	if err := m.Apply(TranscriptChunk{File: "/t/a.jsonl", Off: 4, Next: 8, Data: "{c}\n"}); !errors.Is(err, ErrMirrorGap) {
		t.Errorf("a chunk that doesn't continue the copy: %v", err)
	}

	// A crash after appending, before recording: the extra bytes go.
	f, _ := os.OpenFile(m.dataPath(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{half")
	f.Close()
	m.Close()
	m, err = OpenMirror("i-1")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	lines, _ := m.Lines()
	if meta := m.Meta(); meta.Next != 9000 || meta.Len != 8 || meta.Skipped != 1 || len(lines) != 2 || string(lines[1]) != "{b}" {
		t.Errorf("reopened: %+v %q", meta, lines)
	}

	// A new conversation replaces the copy.
	apply(TranscriptChunk{File: "/t/b.jsonl", Kind: "claude", Off: 0, Next: 4, Data: "{z}\n", Reset: true})
	lines, _ = m.Lines()
	if len(lines) != 1 || !strings.Contains(string(lines[0]), "z") || m.Meta().File != "/t/b.jsonl" {
		t.Errorf("after reset: %q %+v", lines, m.Meta())
	}

	// A second process works on a private copy and leaves the shared one be.
	other, err := OpenMirror("i-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Apply(TranscriptChunk{File: "/t/b.jsonl", Kind: "claude", Off: 4, Next: 8, Data: "{y}\n"}); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if lines, _ = m.Lines(); len(lines) != 1 {
		t.Errorf("the shared copy changed under its keeper: %q", lines)
	}
}

package pier

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/usepier/pier/pkg/pier/wire"
)

func itemLine(id, text string) string {
	b, _ := json.Marshal(wire.ChatItem{ID: id, Kind: wire.ChatAssistant, Text: text})
	return string(b) + "\n"
}

func TestMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m, err := OpenMirror("i-1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(ch LogChunk) {
		t.Helper()
		if err := m.Apply(ch); err != nil {
			t.Fatal(err)
		}
	}
	a, b := itemLine("a", "one"), itemLine("b", "two")
	apply(LogChunk{Chat: "c1", File: "log-1.jsonl", Off: 0, Next: int64(len(a)), Data: a, Reset: true})
	apply(LogChunk{Chat: "c1", File: "log-1.jsonl", Off: int64(len(a)), Next: 9000, Data: b, Skipped: 1})
	if err := m.Apply(LogChunk{File: "log-1.jsonl", Off: 4, Next: 8, Data: a}); !errors.Is(err, ErrMirrorGap) {
		t.Errorf("a chunk that doesn't continue the copy: %v", err)
	}

	// A crash after appending, before recording: the extra bytes go.
	f, _ := os.OpenFile(m.dataPath(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{half")
	f.Close()
	m.Close()
	m, err = OpenMirror("i-1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	items, _ := m.Items()
	if meta := m.Meta(); meta.Next != 9000 || meta.Len != int64(len(a)+len(b)) || meta.Skipped != 1 || len(items) != 2 || items[1].Text != "two" {
		t.Errorf("reopened: %+v %+v", meta, items)
	}
	// A later line of an item is its newer state.
	b2 := itemLine("b", "two, done")
	apply(LogChunk{Chat: "c1", File: "log-1.jsonl", Off: 9000, Next: 9000 + int64(len(b2)), Data: b2})
	if items, _ = m.Items(); len(items) != 2 || items[1].Text != "two, done" {
		t.Errorf("folded: %+v", items)
	}

	// A compacted log replaces the copy.
	z := itemLine("z", "fresh")
	apply(LogChunk{Chat: "c1", File: "log-2.jsonl", Off: 0, Next: int64(len(z)), Data: z, Reset: true})
	items, _ = m.Items()
	if len(items) != 1 || items[0].ID != "z" || m.Meta().File != "log-2.jsonl" {
		t.Errorf("after reset: %+v %+v", items, m.Meta())
	}

	// A second process works on a private copy and leaves the shared one be.
	other, err := OpenMirror("i-1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	y := itemLine("y", "other")
	if err := other.Apply(LogChunk{File: "log-2.jsonl", Off: int64(len(z)), Next: int64(len(z) + len(y)), Data: y}); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if items, _ = m.Items(); len(items) != 1 {
		t.Errorf("the shared copy changed under its keeper: %+v", items)
	}
	if got := ChatMirrors("i-1"); len(got) != 1 || got[0] != "c1" {
		t.Errorf("chat copies = %v", got)
	}
}

// A copy past its size keeps the newest items, once each, and says older
// ones are only in the session.
func TestMirrorCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m, err := OpenMirror("i-2", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	big := strings.Repeat("x", 4<<10)
	var off int64
	for i := range keepItems + 2000 {
		line := itemLine(fmt.Sprint(i), big)
		if err := m.Apply(LogChunk{File: "log-1.jsonl", Off: off, Next: off + int64(len(line)), Data: line, Reset: i == 0}); err != nil {
			t.Fatal(err)
		}
		off += int64(len(line))
	}
	items, _ := m.Items()
	meta := m.Meta()
	if len(items) > keepItems+maxMirror/(4<<10) || !meta.Dropped || meta.Next != off || meta.Len > maxMirror+(4<<10)*2 {
		t.Errorf("capped copy: %d items, %+v", len(items), meta)
	}
	if items[len(items)-1].ID != fmt.Sprint(keepItems+1999) {
		t.Errorf("the newest item is kept: %s", items[len(items)-1].ID)
	}
}

package api

import (
	"io"
	"strings"
	"testing"
)

func TestFindSetupToken(t *testing.T) {
	tok := tokenPrefix + strings.Repeat("aB3_-", 19) // 108 characters
	cases := []struct {
		name, text string
		cols       int
		want       string
		done       bool
	}{
		{"on one line", "Your OAuth token (valid for 1 year):\n\n" + tok + "\n\nStore this token securely.", 120, tok, true},
		{"wrapped at the width", "\n\n" + tok[:80] + "\n" + tok[80:] + "\n\nStore this", 80, tok, true},
		{"wrapped, indented", "\n  " + tok[:78] + "\r\n" + tok[78:] + "\n\nStore", 80, tok, true},
		{"a short line ends it", "\n" + tok[:60] + "\nStore", 80, tok[:60], true},
		{"still printing", "\n" + tok[:50], 120, "", false},
		{"cut at a full line, more to come", "\n" + tok[:80] + "\n", 80, "", false},
		{"not there", "Opening the browser…", 80, "", false},
	}
	for _, c := range cases {
		got, done := findSetupToken(c.text, c.cols)
		if got != c.want || done != c.done {
			t.Errorf("%s: %q %v, want %q %v", c.name, got, done, c.want, c.done)
		}
	}
	// Indented by two, the wrap comes at the width counted from the line's
	// start.
	text := "\n  " + tok[:78] + "\n" + tok[78:] + "\n\n"
	if got, _ := findSetupToken(text, 80); got != tok {
		t.Errorf("indented wrap = %q", got)
	}
}

// chunkTerm prints its chunks, one per read.
type chunkTerm struct {
	chunks []string
	done   chan struct{}
}

func (c *chunkTerm) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks = c.chunks[1:]
	return n, nil
}
func (c *chunkTerm) Write(p []byte) (int, error) { return len(p), nil }
func (c *chunkTerm) Resize(int, int) error       { return nil }
func (c *chunkTerm) Done() <-chan struct{}       { return c.done }
func (c *chunkTerm) ExitCode() int               { return 0 }
func (c *chunkTerm) Close() error                { return nil }

func TestTokenTermFindsItAcrossReadsAndEscapes(t *testing.T) {
	tok := tokenPrefix + strings.Repeat("Zx9", 32)
	ct := &chunkTerm{chunks: []string{
		"\x1b[1mYour OAuth token\x1b[0m:\r\n\r\n\x1b[32m" + tok[:30],
		tok[30:] + "\x1b[0m\r\n\r\nStore this token securely.",
	}}
	var got []string
	tt := &tokenTerm{term: ct, cols: 200, found: func(s string) { got = append(got, s) }}
	buf := make([]byte, 4096)
	for {
		if _, err := tt.Read(buf); err != nil {
			break
		}
	}
	if len(got) != 1 || got[0] != tok {
		t.Errorf("found %q", got)
	}
}

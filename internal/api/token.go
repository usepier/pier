package api

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier/wire"
)

// `claude setup-token` prints the token sessions sign in with. pier reads
// it from the terminal as it goes by and keeps it in pier's config, so the
// user never copies it and the app never holds it.

const tokenPrefix = "sk-ant-oat01-"

func tokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// findSetupToken finds the token in a terminal's text (escapes stripped),
// joining it across the line breaks a cols-wide terminal wrapped it at.
// done is false while the text may still be cut short.
func findSetupToken(text string, cols int) (tok string, done bool) {
	i := strings.LastIndex(text, tokenPrefix)
	if i < 0 {
		return "", false
	}
	var b strings.Builder
	col := i - (strings.LastIndexByte(text[:i], '\n') + 1)
	for j := i; ; {
		for j < len(text) && tokenChar(text[j]) {
			b.WriteByte(text[j])
			j++
			col++
		}
		if j == len(text) {
			return "", false // more may come
		}
		// A break where the line was full is the terminal's, not the
		// token's end.
		if text[j] == '\r' || text[j] == '\n' {
			k := j
			for k < len(text) && (text[k] == '\r' || text[k] == '\n') {
				k++
			}
			if strings.Count(text[j:k], "\n") == 1 && cols > 0 && col >= cols {
				if k == len(text) {
					return "", false // the rest of a wrapped token may follow
				}
				if tokenChar(text[k]) {
					j, col = k, 0
					continue
				}
			}
		}
		break
	}
	if b.Len() < len(tokenPrefix)+40 {
		return "", true
	}
	return b.String(), true
}

// tokenTerm is a terminal running `claude setup-token`, watched for the
// token.
type tokenTerm struct {
	term
	found func(string)

	mu   sync.Mutex
	cols int
	text strings.Builder
	got  bool
}

func (t *tokenTerm) Read(p []byte) (int, error) {
	n, err := t.term.Read(p)
	if n > 0 {
		t.mu.Lock()
		var tok string
		if !t.got {
			t.text.WriteString(ansi.Strip(string(p[:n])))
			if t.text.Len() > 64<<10 {
				keep := t.text.String()[t.text.Len()-16<<10:]
				t.text.Reset()
				t.text.WriteString(keep)
			}
			if found, done := findSetupToken(t.text.String(), t.cols); done && found != "" {
				tok, t.got = found, true
			}
		}
		t.mu.Unlock()
		if tok != "" {
			t.found(tok)
		}
	}
	return n, err
}

func (t *tokenTerm) Resize(cols, rows int) error {
	t.mu.Lock()
	t.cols = cols
	t.mu.Unlock()
	return t.term.Resize(cols, rows)
}

// keepClaudeToken keeps a token `claude setup-token` printed: in pier's
// config, or — before setup has written one — for setup to.
func (s *Server) keepClaudeToken(termID, tok string) {
	s.mu.Lock()
	s.claudeToken = tok
	s.mu.Unlock()
	if _, err := config.Load(); err == nil {
		if _, err := config.Update(func(c *config.Config) error {
			c.Secrets.ClaudeOAuthToken = tok
			return nil
		}); err != nil {
			s.logf("keeping the Claude token: %v", err)
			return
		}
		// New sessions start as background pier jobs, which read the
		// config afresh: nothing here needs rebuilding.
	}
	s.notify("term.captured", wire.TermCaptured{TermID: termID, What: "claude-token"})
}

package pier

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
)

var uploadExt = regexp.MustCompile(`^\.[A-Za-z0-9]{1,8}$`)

// maxUpload bounds a file put into a session through the chat.
const maxUpload = 20 << 20

// Upload puts a file (an image pasted into the chat) into the session and
// returns its path there, for the prompt to name.
func (c *Client) Upload(ctx context.Context, s Session, name string, data []byte) (string, error) {
	if len(data) > maxUpload {
		return "", fmt.Errorf("%s is over %d MB", name, maxUpload>>20)
	}
	conn, err := c.Conn(ctx, s)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !uploadExt.MatchString(ext) {
		ext = ""
	}
	b := make([]byte, 8)
	rand.Read(b)
	file := hex.EncodeToString(b) + ext
	out, err := conn.Exec(ctx, `mkdir -p ~/.pier/uploads && cat > ~/.pier/uploads/`+file+` && echo "$HOME/.pier/uploads/`+file+`"`, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// maxListed bounds a session's file list.
const maxListed = 50000

// ListFiles lists the session checkout's files (tracked and untracked,
// ignored ones left out), relative to the repo root, for @-mentions.
func (c *Client) ListFiles(ctx context.Context, s Session) (paths []string, truncated bool, err error) {
	if err := CheckReady(s); err != nil {
		return nil, false, err
	}
	out, err := c.exec(ctx, s.ID, "cd ~/work/"+shellQuote(s.Repo)+" && git ls-files -z --cached --others --exclude-standard | head -c 8000000")
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
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
	return paths, len(out) >= 8000000, nil
}

// mimeOf is a file's media type by its extension ("" when unknown).
func mimeOf(name string) string {
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
}

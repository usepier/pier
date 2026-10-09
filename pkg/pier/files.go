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

// mimeOf is a file's media type by its extension ("" when unknown).
func mimeOf(name string) string {
	return mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
}

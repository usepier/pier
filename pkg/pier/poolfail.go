package pier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usepier/pier/internal/config"
)

// A fill that fails is remembered until one succeeds, so a short pool says
// why it's short — not only that it is — wherever it's shown, however long
// ago the fill ran.

// PoolFailure is the last fill of a repo's pool, when it failed.
type PoolFailure struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"` // its first line; the pool's log has the rest
}

func poolFailurePath(repo string) string {
	return filepath.Join(config.Dir(), "pool", repo+".failed.json")
}

// recordFill keeps a fill's outcome: a failure replaces the last one, a
// success clears it. A fill that was only interrupted says nothing.
func recordFill(repo string, err error) {
	path := poolFailurePath(repo)
	if err == nil {
		_ = os.Remove(path)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	msg = strings.TrimSuffix(strings.TrimPrefix(msg, "pool member: "), ":")
	b, _ := json.Marshal(PoolFailure{At: time.Now().UTC(), Message: msg})
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
}

// LastPoolFailure is the repo's last fill, if it failed.
func LastPoolFailure(repo string) *PoolFailure {
	b, err := os.ReadFile(poolFailurePath(repo))
	if err != nil {
		return nil
	}
	var f PoolFailure
	if json.Unmarshal(b, &f) != nil || f.Message == "" {
		return nil
	}
	return &f
}

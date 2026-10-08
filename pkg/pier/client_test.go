package pier

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
)

// A login error stays recognizable however deep a caller wrapped it, and
// yields the login that fixes it; nothing else does.
func TestReauthCommandFollowsTheError(t *testing.T) {
	c := &Client{}
	expired := fmt.Errorf("listing sessions: %w", &driver.LoginError{
		Login: []string{"gcloud", "auth", "login"},
		Err:   errors.New("gcloud auth has expired"),
	})
	if !IsAuthExpired(expired) {
		t.Fatal("a wrapped login error must read as expired")
	}
	if cmd := c.ReauthCommand(expired); cmd == nil || strings.Join(cmd.Args, " ") != "gcloud auth login" {
		t.Errorf("ReauthCommand = %v, want gcloud auth login", cmd)
	}

	other := errors.New("aws ec2 describe-instances: access denied")
	if IsAuthExpired(other) || c.ReauthCommand(other) != nil {
		t.Error("other errors must not offer a login")
	}
}

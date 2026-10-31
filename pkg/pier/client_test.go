package pier

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/config"
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

// A long-lived client (pier api, the TUI) sees what other processes write
// to the config — a bake's background finish recording its image — and
// says it needs rebuilding only when its cloud or machine settings changed.
func TestClientFollowsTheConfigOnDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := config.Update(func(c *config.Config) error {
		c.AWS.Region = "eu-central-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Update(func(c *config.Config) error {
		c.RecordBake("shop", "ami-1")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := c.Config().BakedImage("shop"); got != "ami-1" {
		t.Errorf("after another process's bake, the client sees image %q", got)
	}
	if c.Stale() {
		t.Error("a recorded bake made the client's driver stale")
	}
	if _, err := config.Update(func(c *config.Config) error {
		c.AWS.InstanceType = "t4g.2xlarge"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !c.Stale() {
		t.Error("a new default machine type left the client's driver as it was")
	}
}

package awsec2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginExpired(t *testing.T) {
	err := errors.New("aws sts get-caller-identity: aws: [ERROR]: Your session has expired. Please reauthenticate using 'aws login'.")
	if !LoginExpired(err) {
		t.Fatal("the AWS login expiry must be recoverable")
	}
	for _, err := range []error{
		nil,
		errors.New("aws ec2 describe-instances: access denied"),
		errors.New("The SSO session associated with this profile has expired or is otherwise invalid"),
	} {
		if LoginExpired(err) {
			t.Errorf("LoginExpired(%v) = true; only `aws login` expiry is supported", err)
		}
	}
}

func TestLoginCommand(t *testing.T) {
	if got := LoginCommand("").Args; strings.Join(got, " ") != "aws login" {
		t.Fatalf("LoginCommand(\"\").Args = %q", got)
	}
	if got := LoginCommand("pier").Args; strings.Join(got, " ") != "aws login --profile pier" {
		t.Fatalf("LoginCommand(\"pier\").Args = %q", got)
	}
}

func TestUserPreservesAssumedRoleSessionIdentity(t *testing.T) {
	const arn = "arn:aws:sts::123456789012:assumed-role/Developer/alice@example.com"
	bin := t.TempDir()
	aws := filepath.Join(bin, "aws")
	if err := os.WriteFile(aws, []byte("#!/bin/sh\nprintf '%s\\n' '"+arn+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	d := Driver{}

	got, err := d.user(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != arn {
		t.Fatalf("user() = %q, want complete role-session ARN %q", got, arn)
	}
}

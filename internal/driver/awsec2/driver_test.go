package awsec2

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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

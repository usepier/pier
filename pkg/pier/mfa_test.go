package pier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A profile that asks for an MFA code says so (with its device) until it
// gets one; a wrong code says so too.
func TestCheckAWSWithMFA(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nread code\necho 'Enter MFA code for arn:aws:iam::111122223333:mfa/dev: ' >&2\n" +
		"if [ \"$code\" = 123456 ]; then echo arn:aws:sts::111122223333:assumed-role/dev/x; exit 0; fi\n" +
		"if [ -n \"$code\" ]; then echo 'An error occurred (AccessDenied) when calling the AssumeRole operation: MultiFactorAuthentication failed' >&2; exit 255; fi\n" +
		"echo 'EOF when reading a line' >&2; exit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	_, err := CheckAWS(ctx, "dev", "")
	var me *MFAError
	if !errors.Is(err, ErrMFARequired) || !errors.As(err, &me) || me.Device != "arn:aws:iam::111122223333:mfa/dev" {
		t.Fatalf("no code = %v", err)
	}
	if _, err := CheckAWS(ctx, "dev", "000000"); err == nil || errors.Is(err, ErrMFARequired) {
		t.Errorf("a wrong code = %v", err)
	}
	if arn, err := CheckAWS(ctx, "dev", " 123456 "); err != nil || arn != "arn:aws:sts::111122223333:assumed-role/dev/x" {
		t.Errorf("the code = %q %v", arn, err)
	}
}

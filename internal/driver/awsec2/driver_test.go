package awsec2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usepier/pier/internal/driver"
)

// Each expired AWS login names its own fix: console-login credentials renew
// with `aws login`, Identity Center sessions with `aws sso login`, both on
// the configured profile. Any other failure is not a login problem.
func TestLoginFix(t *testing.T) {
	for _, c := range []struct {
		stderr string
		want   string
	}{
		{"aws: [ERROR]: Your session has expired. Please reauthenticate using 'aws login'.", "aws login --profile dev"},
		{"The SSO session associated with this profile has expired or is otherwise invalid. To refresh this SSO session run aws sso login with the corresponding profile.", "aws sso login --profile dev"},
		{"Error when retrieving token from sso: Token has expired and refresh failed", "aws sso login --profile dev"},
		{"Error loading SSO Token: Token for my-sso does not exist", "aws sso login --profile dev"},
		{"An error occurred (UnauthorizedOperation) when calling the DescribeInstances operation: access denied", ""},
		{"", ""},
	} {
		if got := strings.Join(loginFix(c.stderr, "dev"), " "); got != c.want {
			t.Errorf("loginFix(%q) = %q, want %q", c.stderr, got, c.want)
		}
	}
	if got := strings.Join(loginFix("Error loading SSO Token", ""), " "); got != "aws sso login" {
		t.Errorf("with no profile configured the login must use the default one, got %q", got)
	}
}

// An expired login reaches every caller as a LoginError carrying its fix,
// not just as text.
func TestAWSFailureCarriesItsLogin(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'The SSO session associated with this profile has expired or is otherwise invalid.' >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	d := Driver{Profile: "dev"}

	_, err := d.aws(context.Background(), "ec2", "describe-instances")
	var le *driver.LoginError
	if !errors.As(fmt.Errorf("listing: %w", err), &le) {
		t.Fatalf("expected a LoginError, got %v", err)
	}
	if got := strings.Join(le.Login, " "); got != "aws sso login --profile dev" {
		t.Errorf("login = %q", got)
	}
	if !strings.Contains(err.Error(), "SSO session") {
		t.Errorf("the CLI's own message must survive: %v", err)
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

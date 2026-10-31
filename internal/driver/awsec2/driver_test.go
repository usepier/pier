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
		{"Enter MFA code for arn:aws:iam::111122223333:mfa/dev: \nEOF when reading a line", "aws sts get-caller-identity --profile dev"},
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

// The list reads a session's origin and base back from its tags.
func TestListReadsOriginAndBase(t *testing.T) {
	bin := t.TempDir()
	list := `[{"id":"i-1","state":"running","itype":"t4g.medium","tags":[` +
		`{"Key":"pier:session","Value":"s"},{"Key":"pier:ready","Value":"1"},` +
		`{"Key":"pier:origin","Value":"github.com/o/r"},{"Key":"pier:base","Value":"main"}]}]`
	script := "#!/bin/sh\ncase \"$2\" in\n" +
		"get-caller-identity) echo arn:aws:iam::1:user/me ;;\n" +
		"describe-instances) echo '" + list + "' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	d := &Driver{StateDir: t.TempDir()}
	got, err := d.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Origin != "github.com/o/r" || got[0].Base != "main" {
		t.Fatalf("List = %+v", got)
	}
}

// Only images carrying your identity are yours: a teammate's, or one with
// no owner tag at all, never are.
func TestImagesAreOnlyYours(t *testing.T) {
	const me = "arn:aws:iam::123456789012:user/kerem"
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
*get-caller-identity*) echo '` + me + `' ;;
*describe-images*) cat <<'JSON'
[["ami-mine","2026-10-01T10:00:00.000Z","shop","` + me + `"],
 ["ami-teammate","2026-10-02T10:00:00.000Z","shop","arn:aws:iam::123456789012:user/alice"],
 ["ami-ownerless","2026-10-03T10:00:00.000Z","legacy",null]]
JSON
;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	imgs, err := (&Driver{}).Images(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].ID != "ami-mine" {
		t.Errorf("images = %+v", imgs)
	}
}

func TestCloudErrorCodes(t *testing.T) {
	cases := []struct{ stderr, code, op string }{
		{"\nAn error occurred (VcpuLimitExceeded) when calling the RunInstances operation: You have requested more vCPU capacity than your current vCPU limit of 32 allows", "VcpuLimitExceeded", "RunInstances"},
		{"An error occurred (InvalidPermission.Duplicate) when calling the AuthorizeSecurityGroupIngress operation: the specified rule already exists", "InvalidPermission.Duplicate", "AuthorizeSecurityGroupIngress"},
		{"An error occurred (NoSuchEntity) when calling the GetRole operation (reached max retries: 2): The role with name pier cannot be found.", "NoSuchEntity", "GetRole"},
		{"Unknown options: --volume-initialization-rate", "", ""},
	}
	for _, c := range cases {
		err := cloudError(c.stderr, errors.New("aws ec2 x: "+c.stderr))
		var ce *driver.CloudError
		if got := driver.ErrorCode(err); got != c.code {
			t.Errorf("%q: code %q, want %q", c.stderr, got, c.code)
		}
		if c.code != "" && (!errors.As(err, &ce) || ce.Op != c.op || ce.Message == "") {
			t.Errorf("%q: %+v", c.stderr, ce)
		}
	}
	if !errors.Is(cloudError(cases[0].stderr, errors.New("x")), driver.ErrQuota) {
		t.Error("a vCPU limit is a quota")
	}
	if errors.Is(cloudError(cases[1].stderr, errors.New("x")), driver.ErrQuota) {
		t.Error("a duplicate rule isn't a quota")
	}
}

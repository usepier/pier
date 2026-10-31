package pier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
)

// Setting pier up on a machine, as steps any frontend can drive: what is
// here already, whether the cloud login works, the defaults new sessions
// get, and the account's groundwork. `pier setup` asks its questions in a
// terminal around these; the Mac app shows them as a form.

// Tool is a command-line tool setup needs.
type Tool struct {
	Name  string // the command
	Hint  string // how to install it
	Found bool
}

// Tools lists what a cloud ("aws-ec2", "gcp-gce"; "" for the common ones)
// needs on this machine.
func Tools(cloud string) []Tool {
	ts := []Tool{{Name: "git", Hint: "install git"}, {Name: "ssh-keygen", Hint: "install OpenSSH"}}
	switch cloud {
	case "aws-ec2":
		ts = append(ts,
			Tool{Name: "aws", Hint: "brew install awscli"},
			Tool{Name: "session-manager-plugin", Hint: "brew install --cask session-manager-plugin"})
	case "gcp-gce":
		ts = append(ts, Tool{Name: "gcloud", Hint: "install the Google Cloud CLI (cloud.google.com/sdk/docs/install)"})
	}
	for i := range ts {
		_, err := exec.LookPath(ts[i].Name)
		ts[i].Found = err == nil
	}
	return ts
}

// Detected is what setup finds on this machine before asking anything.
type Detected struct {
	// Config is the saved config, else the defaults: running setup again
	// keeps earlier answers.
	Config     config.Config
	Configured bool
	// AWSProfiles are the AWS CLI's profiles.
	AWSProfiles []string
	// GCPAccount is gcloud's active login; GCPProject its active project
	// (only ever a default — pier always names its project).
	GCPAccount string
	GCPProject string
	// Agents are the agent config dirs here (".claude", ".codex").
	Agents []string
	// ClaudeAuth says how claude authenticates without a token, when its
	// copied settings carry their own credentials.
	ClaudeAuth string
}

// Detect looks around this machine for setup's defaults.
func Detect() Detected {
	d := Detected{Config: config.Default()}
	if existing, err := config.Load(); err == nil {
		d.Config, d.Configured = existing, true
	}
	if len(d.Config.Secrets.Manifest) == 0 {
		d.Config.Secrets.Manifest = DetectManifest()
	}
	if out, err := exec.Command("aws", "configure", "list-profiles").Output(); err == nil {
		d.AWSProfiles = strings.Fields(string(out))
	}
	d.GCPAccount, _ = GCloudAccount()
	if out, err := exec.Command("gcloud", "config", "get-value", "project").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "(unset)" {
			d.GCPProject = p
		}
	}
	home, _ := os.UserHomeDir()
	d.Agents = AgentDirs(home)
	d.ClaudeAuth = ClaudeSelfContained()
	return d
}

// AWSRegion is the region the AWS CLI uses for a profile, "" if none.
func AWSRegion(profile string) string {
	out, _ := exec.Command("aws", "configure", "get", "region", "--profile", profile).Output()
	return strings.TrimSpace(string(out))
}

// SSOExpired recognizes an AWS CLI error that `aws sso login` fixes.
func SSOExpired(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "sso") && (strings.Contains(low, "expired") || strings.Contains(low, "refresh"))
}

// ErrMFARequired: the AWS profile assumes a role that asks for an MFA
// code (an *MFAError says which device).
var ErrMFARequired = errors.New("the AWS profile asks for an MFA code")

// MFAError is a profile asking for a code from an MFA device.
type MFAError struct {
	Profile string
	Device  string // the MFA device's ARN
}

func (e *MFAError) Error() string {
	return fmt.Sprintf("profile %q asks for a code from %s", e.Profile, e.Device)
}

func (e *MFAError) Is(target error) bool { return target == ErrMFARequired }

var mfaPrompt = regexp.MustCompile(`Enter MFA code for (\S+):(?:\s|$)`)

// CheckAWS proves a profile's credentials work and returns who they are.
// An expired SSO session is a *LoginError carrying the login that fixes it
// (IsAuthExpired). A profile that asks for an MFA code is an *MFAError
// until the code is given (mfaCode); the AWS CLI keeps the session it buys
// for later calls. It never prompts.
func CheckAWS(ctx context.Context, profile, mfaCode string) (string, error) {
	var errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "aws", "sts", "get-caller-identity", "--query", "Arn", "--output", "text", "--profile", profile)
	cmd.Stderr = &errb
	// No terminal to prompt on: the CLI reads a code from stdin, or finds
	// none there.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = strings.NewReader(strings.TrimSpace(mfaCode) + "\n")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	msg := strings.TrimSpace(errb.String())
	if msg == "" {
		msg = err.Error()
	}
	switch {
	case SSOExpired(msg):
		return "", &driver.LoginError{Login: []string{"aws", "sso", "login", "--profile", profile}, Err: fmt.Errorf("the SSO session for profile %q has expired", profile)}
	case mfaPrompt.MatchString(msg):
		if strings.TrimSpace(mfaCode) != "" && strings.Contains(msg, "AccessDenied") {
			return "", fmt.Errorf("AWS didn't take that MFA code for profile %q — try the next one", profile)
		}
		return "", &MFAError{Profile: profile, Device: mfaPrompt.FindStringSubmatch(msg)[1]}
	}
	return "", fmt.Errorf("credentials for profile %q aren't working: %s", profile, msg)
}

// GCloudAccount is gcloud's active login.
func GCloudAccount() (string, error) {
	out, err := exec.Command("gcloud", "auth", "list", "--filter=status:ACTIVE", "--format=value(account)").Output()
	acct, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || acct == "" {
		return "", fmt.Errorf("no active gcloud account")
	}
	return acct, nil
}

// CheckGCPProject proves a project exists and the login can reach it.
func CheckGCPProject(project string) error {
	if project == "" {
		return fmt.Errorf("pier needs a project — create one at console.cloud.google.com")
	}
	if out, err := exec.Command("gcloud", "projects", "describe", project, "--format", "value(projectId)").CombinedOutput(); err != nil {
		return fmt.Errorf("cannot access project %q: %s", project, strings.TrimSpace(string(out)))
	}
	return nil
}

// CheckGCP proves the gcloud login works and reaches the project; returns
// the account. No login is a *LoginError with `gcloud auth login`.
func CheckGCP(project string) (string, error) {
	acct, err := GCloudAccount()
	if err != nil {
		return "", &driver.LoginError{Login: []string{"gcloud", "auth", "login"}, Err: err}
	}
	return acct, CheckGCPProject(project)
}

// ClaudeSelfContained reports whether ~/.claude/settings.json's env block
// alone authenticates claude on a fresh VM (a Foundry deployment, an API
// key — the file travels with the manifest). Bedrock, Vertex and Entra need
// cloud credentials that deliberately never enter sessions, so those still
// want a token.
func ClaudeSelfContained() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	switch {
	case s.Env["CLAUDE_CODE_USE_FOUNDRY"] == "1" && s.Env["ANTHROPIC_FOUNDRY_API_KEY"] != "":
		return "foundry deployment + api key"
	case s.Env["ANTHROPIC_API_KEY"] != "":
		return "anthropic api key"
	case s.Env["ANTHROPIC_AUTH_TOKEN"] != "":
		return "auth token"
	}
	return ""
}

// NeedsClaudeToken reports whether sessions need a Claude token: claude's
// config travels, but its login lives in the macOS Keychain.
func NeedsClaudeToken(cfg config.Config) bool {
	if cfg.Secrets.ClaudeOAuthToken != "" {
		return false
	}
	if ClaudeSelfContained() != "" && slices.Contains(cfg.Secrets.Manifest, ".claude/settings.json") {
		return false
	}
	return slices.ContainsFunc(cfg.Secrets.Manifest, func(m string) bool { return strings.HasPrefix(m, ".claude") })
}

// DetectManifest proposes $HOME-relative agent config worth carrying into
// sessions (only entries that exist).
func DetectManifest() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		".claude/CLAUDE.md",
		".claude/settings.json",
		".claude/agents",
		".claude/commands",
		".claude/skills",
		".claude/plugins",
		".claude/.credentials.json", // Linux; macOS uses the token flow
		".codex/config.toml",
		".codex/auth.json",
		".codex/AGENTS.md",
		".codex/prompts",
		".codex/skills",
		// NOT .codex/plugins: a machine-specific runtime cache (hundreds of
		// MB); config.toml declares the plugins and codex re-materializes.
		".tmux.conf", // absent → bootstrap seeds mouse mode + deep history
	}
	var found []string
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(home, c)); err == nil {
			found = append(found, c)
		}
	}
	return found
}

// DefaultRow is one default new sessions get, with what it means.
type DefaultRow struct {
	Key   string // the config key that sets it ("" for the skill row)
	Label string
	Value string
	Note  string
}

// MachineKey and DiskKey are the cloud's config keys for the session
// machine and disk.
func MachineKey(cfg config.Config) string {
	if cfg.Driver == "gcp-gce" {
		return "gcp.machine_type"
	}
	return "aws.instance_type"
}

func DiskKey(cfg config.Config) string {
	if cfg.Driver == "gcp-gce" {
		return "gcp.disk_gib"
	}
	return "aws.disk_gib"
}

// DefaultRows lists every default a new session uses, with its cost, so a
// confirm is informed. agents are the agent dirs found; skills whether the
// onboarding skill is to be installed for them.
func DefaultRows(cfg config.Config, drv driver.Driver, agents []string, skills bool) []DefaultRow {
	machine, disk := cfg.AWS.InstanceType, cfg.AWS.DiskGiB
	if cfg.Driver == "gcp-gce" {
		machine, disk = cfg.GCP.MachineType, cfg.GCP.DiskGiB
	}
	rows := []DefaultRow{
		{MachineKey(cfg), "machine", machine, MachineNote(drv, machine)},
		{DiskKey(cfg), "disk", fmt.Sprintf("%d GiB", disk), fmt.Sprintf("the part that survives parking, ~$%.0f/mo while parked", DiskMonthlyFor(cfg, disk))},
		{"idle_timeout", "park after", cfg.IdleTimeout + " idle", "detached and quiet this long → the VM stops, the disk stays"},
		{"unattended_cap", "runaway cap", cfg.UnattendedCap, "parks even a busy agent once you've been away this long"},
		{"speed", "speed", ProfileLabel(cfg.Profile()), ProfileNote(cfg, cfg.Profile(), disk)},
		{"secrets.manifest", "copied in", ManifestSummary(cfg.Secrets.Manifest), "agent config each session starts with"},
	}
	if len(agents) > 0 {
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = strings.TrimPrefix(a, ".")
		}
		state := "install for " + strings.Join(names, ", ")
		if !skills {
			state = "skip"
		}
		rows = append(rows, DefaultRow{"", "agent skill", state, "pier-onboard teaches your agent to set a repo up for pier"})
	}
	return rows
}

// MachineNote describes a machine type in a few words.
func MachineNote(drv driver.Driver, machine string) string {
	for _, m := range drv.Machines(machine) {
		if m.Type == machine {
			return fmt.Sprintf("%s vCPU · %s GB · %s — resize any session later", m.CPU, m.Mem, m.Cost)
		}
	}
	return "resize any session later"
}

// DiskMonthlyFor is what a parked session's disk costs a month.
func DiskMonthlyFor(cfg config.Config, gib int) float64 {
	if cfg.Driver == "gcp-gce" {
		return float64(gib) * 0.10
	}
	return float64(gib) * 0.095
}

// ProfileLabel names a speed profile.
func ProfileLabel(p string) string {
	switch p {
	case "fast":
		return "Fast"
	case "lean":
		return "Lean"
	case "minimal":
		return "Minimal"
	}
	return "Custom"
}

// ProfileNote says what a speed profile does and costs while idle.
func ProfileNote(cfg config.Config, p string, disk int) string {
	switch p {
	case "fast":
		return fmt.Sprintf("image + a pool of 1 per repo · new sessions in ~25s · ~$%.0f/mo per repo idle", DiskMonthlyFor(cfg, disk)+2)
	case "lean":
		return "image per repo · new sessions in ~1-2 min · ~$2/mo per repo idle"
	case "minimal":
		return "nothing stored · every session runs the full setup · $0 idle"
	}
	on := "off"
	if cfg.Speed.BakeReminders {
		on = "on"
	}
	return fmt.Sprintf("a pool of %d per repo, bake reminders %s", cfg.Speed.PoolSize, on)
}

// ManifestSummary names what's copied in, briefly: "Claude, Codex, tmux config".
func ManifestSummary(m []string) string {
	if len(m) == 0 {
		return "nothing"
	}
	var parts []string
	seen := map[string]bool{}
	for _, e := range m {
		name := e
		switch {
		case strings.HasPrefix(e, ".claude"):
			name = "Claude"
		case strings.HasPrefix(e, ".codex"):
			name = "Codex"
		case e == ".tmux.conf":
			name = "tmux"
		}
		if !seen[name] {
			seen[name] = true
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ") + " config"
}

// SetupError is groundwork that failed for a reason the user can act on.
type SetupError struct {
	// Kind: no_vpc (the account has no default VPC: name a subnet),
	// billing (the GCP project has no billing account), permission (the
	// login lacks admin rights: an admin runs AdminDoc).
	Kind string
	Err  error
}

func (e *SetupError) Error() string { return e.Err.Error() }
func (e *SetupError) Unwrap() error { return e.Err }

// Groundwork lays down what the account needs once — the role, profile and
// security group on AWS; APIs and firewall rules on GCP — finding what's
// already there.
func Groundwork(ctx context.Context, cfg config.Config, opts Options) (driver.SetupReport, error) {
	drv, err := NewDriver(cfg, opts)
	if err != nil {
		return driver.SetupReport{}, err
	}
	rep, err := drv.SetupOnce(ctx)
	if err == nil {
		return rep, nil
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(err.Error(), "no default VPC"):
		return rep, &SetupError{Kind: "no_vpc", Err: err}
	case strings.Contains(low, "billing"):
		return rep, &SetupError{Kind: "billing", Err: err}
	case strings.Contains(low, "accessdenied") || strings.Contains(low, "not authorized") || strings.Contains(low, "permission"):
		return rep, &SetupError{Kind: "permission", Err: err}
	}
	return rep, err
}

// SaveSetup writes what setup asks about — and only that: anything else in
// the config file (written meanwhile by a bake finishing elsewhere, say)
// stays as it is on disk.
func SaveSetup(cfg config.Config) error {
	_, err := config.Update(func(c *config.Config) error {
		c.Driver = cfg.Driver
		c.IdleTimeout, c.UnattendedCap = cfg.IdleTimeout, cfg.UnattendedCap
		c.Secrets = cfg.Secrets
		c.Speed = cfg.Speed
		c.AWS.Profile, c.AWS.Region = cfg.AWS.Profile, cfg.AWS.Region
		c.AWS.InstanceType, c.AWS.DiskGiB, c.AWS.Subnet = cfg.AWS.InstanceType, cfg.AWS.DiskGiB, cfg.AWS.Subnet
		c.GCP.Project, c.GCP.Zone = cfg.GCP.Project, cfg.GCP.Zone
		c.GCP.MachineType, c.GCP.DiskGiB = cfg.GCP.MachineType, cfg.GCP.DiskGiB
		return nil
	})
	return err
}

// SetupResult is what ApplySetup did.
type SetupResult struct {
	Created []string
	Existed []string
	Checks  []Check
	// Skills are the agent dirs the onboarding skill went into.
	Skills []string
	// Adopted maps repos to session images found in the account; Deleted
	// are superseded images removed.
	Adopted map[string]string
	Deleted []string
}

// ApplySetup is setup's last step in one call: groundwork, the config,
// the checks, the onboarding skill for the agents named, and the
// account's existing session images taken into the config.
func ApplySetup(ctx context.Context, cfg config.Config, opts Options, skillsFor []string) (SetupResult, error) {
	var res SetupResult
	rep, err := Groundwork(ctx, cfg, opts)
	if err != nil {
		return res, err
	}
	res.Created, res.Existed = rep.Created, rep.Existed
	if err := SaveSetup(cfg); err != nil {
		return res, err
	}
	drv, err := NewDriver(cfg, opts)
	if err != nil {
		return res, err
	}
	res.Checks = drv.Doctor(ctx)
	home, _ := os.UserHomeDir()
	for _, agent := range skillsFor {
		if !slices.Contains(AgentDirs(home), agent) {
			continue
		}
		if _, err := SyncSkills(filepath.Join(home, agent, "skills")); err != nil {
			return res, fmt.Errorf("installing the pier-onboard skill in ~/%s/skills: %w", agent, err)
		}
		res.Skills = append(res.Skills, agent)
	}
	c, err := Open(opts)
	if err != nil {
		return res, err
	}
	imgs, err := c.ReconcileImages(ctx)
	res.Adopted, res.Deleted = imgs.Adopted, imgs.Deleted
	return res, err
}

// AdminDoc is what an admin runs when the person setting pier up can't
// create the groundwork themselves: the same resources SetupOnce makes.
func AdminDoc(cloud string) string {
	if cloud == "gcp-gce" {
		return gcpAdminDoc
	}
	return awsAdminDoc
}

var errNoCloud = errors.New("pick a cloud: aws or gcp")

// CloudDriver reads the answer to "which cloud": aws or gcp, in any of
// their names.
func CloudDriver(answer string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "aws", "aws-ec2":
		return "aws-ec2", nil
	case "gcp", "gcp-gce", "gce", "google":
		return "gcp-gce", nil
	}
	return "", errNoCloud
}

const awsAdminDoc = `# pier groundwork on AWS — run with an account that can manage IAM + EC2.
# Everything pier creates is tagged pier:managed=1. Removal: pier teardown.

aws iam create-role --role-name pier-session \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
  --description "pier session instances: SSM access only" --tags Key=pier:managed,Value=1
aws iam attach-role-policy --role-name pier-session \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam create-instance-profile --instance-profile-name pier-session --tags Key=pier:managed,Value=1
aws iam add-role-to-instance-profile --instance-profile-name pier-session --role-name pier-session
aws ec2 create-security-group --group-name pier-egress-only \
  --description "pier sessions: egress only, zero inbound" --vpc-id <default-vpc-id> \
  --tag-specifications 'ResourceType=security-group,Tags=[{Key=pier:managed,Value=1}]'

# Devs then need permissions for: ec2 run/start/stop/terminate/describe*,
# ec2 create-tags (create's last act marks the session ready via tag —
# without it every create fails and self-terminates),
# ssm start-session + get-parameter, sts get-caller-identity,
# iam:PassRole on pier-session (plus iam get-role + get-instance-profile,
# read by pier doctor), and for direct connect (the default)
# ec2 describe-security-group-rules + authorize/revoke-security-group-ingress
# (pier keeps one TCP-22 rule per caller, scoped to their current public
# IP /32; aws.direct = false forces the SSM tunnel and needs none of it).
# pier resize adds ec2 modify-instance-attribute. pier bake adds
# ec2 create-image + deregister-image + delete-snapshot. The vCPU headroom
# display reads servicequotas get-service-quota (optional, degrades politely).
`

const gcpAdminDoc = `# pier groundwork on GCP — run with a project owner or editor.
# Removal: pier teardown.

gcloud services enable compute.googleapis.com iap.googleapis.com --project <project>
gcloud compute firewall-rules create pier-allow-iap-ssh \
  --project <project> --network default \
  --direction INGRESS --action ALLOW --rules tcp:22 \
  --source-ranges 35.235.240.0/20 --priority 999 --target-tags pier-session
# The deny outranks permissive shared-network rules (default-allow-ssh is
# open to the world) for pier VMs only.
gcloud compute firewall-rules create pier-deny-ingress \
  --project <project> --network default \
  --direction INGRESS --action DENY --rules all \
  --source-ranges 0.0.0.0/0 --priority 1000 --target-tags pier-session

# Devs then need roles/compute.instanceAdmin.v1 (instances, disks, images,
# metadata, labels) and roles/iap.tunnelResourceAccessor (the SSH tunnel).
# Sessions run with no service account, so no serviceAccountUser grant is
# needed. The CPU headroom display reads compute regions describe (covered
# by instanceAdmin).
`

// Where sessions can run when the cloud can't be asked (no login yet).
var (
	knownAWSRegions = []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2", "ca-central-1", "sa-east-1",
		"eu-central-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-north-1", "eu-south-1",
		"ap-northeast-1", "ap-northeast-2", "ap-south-1", "ap-southeast-1", "ap-southeast-2"}
	knownGCPZones = []string{"us-central1-a", "us-central1-b", "us-east1-b", "us-east4-a", "us-west1-a",
		"europe-west1-b", "europe-west3-a", "europe-west4-a", "europe-north1-a",
		"asia-northeast1-a", "asia-southeast1-a", "australia-southeast1-a"}
)

// Regions are where sessions can run: the AWS regions a profile's account
// has, or the zones a GCP project has — the usual ones when the cloud
// can't be asked.
func Regions(ctx context.Context, cloud, profile, project string) []string {
	var cmd *exec.Cmd
	fallback := knownAWSRegions
	switch cloud {
	case "aws-ec2":
		if profile == "" {
			profile = "default"
		}
		cmd = exec.CommandContext(ctx, "aws", "ec2", "describe-regions", "--query", "Regions[].RegionName", "--output", "text", "--profile", profile, "--region", "us-east-1")
	case "gcp-gce":
		fallback = knownGCPZones
		args := []string{"compute", "zones", "list", "--format=value(name)"}
		if project != "" {
			args = append(args, "--project", project)
		}
		cmd = exec.CommandContext(ctx, "gcloud", args...)
	default:
		return nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.Output()
	if err != nil {
		return fallback
	}
	list := strings.Fields(string(out))
	if len(list) == 0 {
		return fallback
	}
	slices.Sort(list)
	return list
}

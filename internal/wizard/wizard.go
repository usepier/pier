// Package wizard implements `pier setup`: cloud and account → every default
// shown for confirm or edit → groundwork and checks → the offer to bake the
// current repo. Everything detected becomes a prefilled default, so a second
// dev on a prepared account just presses enter a few times.
package wizard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

// adminDoc is for devs without IAM rights: the exact groundwork their admin
// must create (equivalent to what SetupOnce does).
const adminDoc = `# pier groundwork on AWS — run with an account that can manage IAM + EC2.
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

// Run is `pier setup`: cloud and account, then every default shown in one
// block for the user to confirm or edit (nothing is applied silently), the
// account groundwork, checks, and — inside a repo — the offer to bake its
// session image. It ends by pointing at the settings page, where all of it
// can change later.
func Run(opts pier.Options, printAdminOnly bool) error {
	if printAdminOnly {
		// The doc matches the configured cloud; without a config, AWS.
		if cfg, err := config.Load(); err == nil && cfg.Driver == "gcp-gce" {
			fmt.Print(gcpAdminDoc)
		} else {
			fmt.Print(adminDoc)
		}
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	fmt.Println("\n " + ui.Title.Render("⚓ pier setup") +
		ui.Dim.Render("  sessions run on your own cloud account; nothing leaves it"))
	fmt.Println()
	for bin, hint := range map[string]string{
		"ssh-keygen": "install OpenSSH",
		"git":        "install git",
	} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found — %s, then re-run `pier setup`", bin, hint)
		}
	}

	cfg := config.Default()
	if existing, err := config.Load(); err == nil {
		cfg = existing // re-running keeps previous answers as defaults
	}

	// 1. cloud and account — the only questions without a default answer.
	fmt.Println(" " + ui.Accent.Render("1 · cloud"))
	cloudDef := "aws"
	if cfg.Driver == "gcp-gce" {
		cloudDef = "gcp"
	}
	var groundwork string
	switch strings.ToLower(ask(in, "cloud (aws / gcp)", cloudDef)) {
	case "aws", "aws-ec2":
		cfg.Driver = "aws-ec2"
		if err := askAWS(in, &cfg); err != nil {
			return err
		}
		groundwork = "IAM role + instance profile + egress-only security group"
	case "gcp", "gcp-gce", "gce", "google":
		cfg.Driver = "gcp-gce"
		if err := askGCP(in, &cfg); err != nil {
			return err
		}
		groundwork = "compute + IAP APIs, IAP-only firewall rules"
	default:
		return fmt.Errorf("unknown cloud — aws or gcp")
	}
	drv, err := pier.NewDriver(cfg, opts)
	if err != nil {
		return err
	}

	// 2. defaults — shown together, confirmed or edited, never assumed.
	if len(cfg.Secrets.Manifest) == 0 {
		cfg.Secrets.Manifest = detectManifest()
	}
	home, _ := os.UserHomeDir()
	agents := AgentDirs(home)
	skills := len(agents) > 0
	fmt.Println("\n " + ui.Accent.Render("2 · defaults") + ui.Dim.Render("  for new sessions — all of it changes later in settings"))
	printDefaults(cfg, drv, agents, skills)
	switch strings.ToLower(ask(in, "use these? [Y]es · [e]dit", "")) {
	case "e", "edit":
		editDefaults(in, &cfg, drv)
		skills = false // edited: the per-agent prompts below decide instead
	}
	if cfg.Secrets.ClaudeOAuthToken == "" {
		if p := claudeSelfContained(); p != "" && slices.Contains(cfg.Secrets.Manifest, ".claude/settings.json") {
			fmt.Printf("  %s claude auth: %s in ~/.claude/settings.json %s\n",
				ui.Mark(true), p, ui.Dim.Render("— travels with the copied config, no token needed"))
		} else if slices.ContainsFunc(cfg.Secrets.Manifest, func(m string) bool { return strings.HasPrefix(m, ".claude") }) {
			fmt.Println(ui.Dim.Render("  Claude subscription auth lives in the macOS Keychain and can't be copied."))
			fmt.Println(ui.Dim.Render("  Run `claude setup-token` in another terminal to mint a token for sessions."))
			if tok := ask(in, "paste token (enter to skip)", ""); tok != "" {
				cfg.Secrets.ClaudeOAuthToken = tok
			}
		}
	}

	// 3. groundwork
	fmt.Println("\n " + ui.Accent.Render("3 · account") + ui.Dim.Render("  "+groundwork))
	rep, err := drv.SetupOnce(ctx)
	if err != nil && strings.Contains(err.Error(), "no default VPC") {
		// Enterprise accounts routinely delete the default VPC. Without this
		// prompt setup would abort before writing config, and the only other
		// place to set aws.subnet is a config file that does not exist yet.
		fmt.Println("  "+ui.Mark(false), err.Error())
		if subnet := ask(in, "subnet for sessions (subnet-...)", ""); subnet != "" {
			cfg.AWS.Subnet = subnet
			if drv, err = pier.NewDriver(cfg, opts); err != nil {
				return err
			}
			rep, err = drv.SetupOnce(ctx)
		}
	}
	if err != nil {
		low := strings.ToLower(err.Error())
		// Fresh GCP projects routinely have no billing account; enabling
		// compute fails on it with a mouthful.
		if strings.Contains(low, "billing") {
			return fmt.Errorf("%w\n\nthe project has no billing account — link one (console.cloud.google.com/billing), then re-run `pier setup`", err)
		}
		// Only blame admin rights when it actually is a permissions error.
		if strings.Contains(low, "accessdenied") || strings.Contains(low, "not authorized") || strings.Contains(low, "permission") {
			return fmt.Errorf("%w\n\nno admin rights? `pier setup --print-admin` prints the commands for your admin", err)
		}
		return err
	}
	for _, c := range rep.Created {
		fmt.Println("  " + ui.OK.Render("+") + " created " + c)
	}
	for _, e := range rep.Existed {
		fmt.Println(ui.Dim.Render("  = found " + e))
	}

	// Write only what the wizard asked about. The prompts above can take
	// minutes, and cfg was read before them — saving it wholesale would
	// revert anything written meanwhile, including a `pier bake` finishing in
	// another terminal. Untouched settings (connection mode, the image maps)
	// come from disk, not from this stale copy.
	if _, err := config.Update(func(c *config.Config) error {
		c.Driver = cfg.Driver
		c.IdleTimeout, c.UnattendedCap = cfg.IdleTimeout, cfg.UnattendedCap
		c.Secrets = cfg.Secrets
		c.Speed = cfg.Speed
		c.AWS.Profile, c.AWS.Region = cfg.AWS.Profile, cfg.AWS.Region
		c.AWS.InstanceType, c.AWS.DiskGiB, c.AWS.Subnet = cfg.AWS.InstanceType, cfg.AWS.DiskGiB, cfg.AWS.Subnet
		c.GCP.Project, c.GCP.Zone = cfg.GCP.Project, cfg.GCP.Zone
		c.GCP.MachineType, c.GCP.DiskGiB = cfg.GCP.MachineType, cfg.GCP.DiskGiB
		return nil
	}); err != nil {
		return err
	}
	fmt.Println(ui.Dim.Render("  wrote " + config.Path()))
	for _, c := range drv.Doctor(ctx) {
		line := "  " + ui.Mark(c.OK) + " " + c.Name
		if c.Detail != "" {
			line += ui.Dim.Render(" — " + c.Detail)
		}
		fmt.Println(line)
	}
	if skills {
		if err := installChosen(&cfg, home, agents); err != nil {
			return err
		}
	} else if len(agents) > 0 {
		if err := offerSkills(in, &cfg, home); err != nil {
			return err
		}
	}

	// 4. this repo's session image — offered, never assumed.
	if repo := gitToplevel(); repo != "" {
		if err := offerBake(ctx, in, opts, repo); err != nil {
			return err
		}
	}

	fmt.Println("\n " + ui.OK.Render("✓ pier is ready"))
	fmt.Println("   start a session    " + ui.Accent.Render("cd <repo> && pier <branch>"))
	fmt.Println("   change anything    " + ui.Accent.Render("run `pier`, press s for settings"))
	fmt.Println()
	return nil
}

// printDefaults shows every default a new session will use, with what it
// costs, so the confirm that follows is informed.
func printDefaults(cfg config.Config, drv driver.Driver, agents []string, skills bool) {
	row := func(label, value, note string) {
		fmt.Printf("    %-14s %s %s\n", label, value, ui.Dim.Render(note))
	}
	machine, disk := cfg.AWS.InstanceType, cfg.AWS.DiskGiB
	if cfg.Driver == "gcp-gce" {
		machine, disk = cfg.GCP.MachineType, cfg.GCP.DiskGiB
	}
	row("machine", machine, machineNote(drv, machine))
	row("disk", fmt.Sprintf("%d GiB", disk), fmt.Sprintf("the part that survives parking, ~$%.0f/mo while parked", diskMonthly(cfg, disk)))
	row("park after", cfg.IdleTimeout+" idle", "detached and quiet this long → the VM stops, the disk stays")
	row("runaway cap", cfg.UnattendedCap, "parks even a busy agent once you've been away this long")
	row("speed", profileLabel(cfg.Profile()), profileNote(cfg, cfg.Profile(), disk))
	row("copied in", manifestSummary(cfg.Secrets.Manifest), "agent config each session starts with")
	if len(agents) > 0 {
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = strings.TrimPrefix(a, ".")
		}
		state := "install for " + strings.Join(names, ", ")
		if !skills {
			state = "skip"
		}
		row("agent skill", state, "pier-onboard teaches your agent to set a repo up for pier")
	}
	fmt.Println()
}

// editDefaults walks each default with its current value preselected; enter
// keeps it.
func editDefaults(in *bufio.Reader, cfg *config.Config, drv driver.Driver) {
	machine := &cfg.AWS.InstanceType
	disk := &cfg.AWS.DiskGiB
	if cfg.Driver == "gcp-gce" {
		machine, disk = &cfg.GCP.MachineType, &cfg.GCP.DiskGiB
	}
	if ms := drv.Machines(*machine); len(ms) > 0 {
		fmt.Println(ui.Dim.Render("    machines:"))
		for i, m := range ms {
			fmt.Printf("      %s %-14s %s vCPU · %s GB · %s\n", ui.Dim.Render(fmt.Sprintf("%d", i+1)), m.Type, m.CPU, m.Mem, m.Cost)
		}
		pick := ask(in, "machine (number or type)", *machine)
		if n, err := strconv.Atoi(pick); err == nil && n >= 1 && n <= len(ms) {
			pick = ms[n-1].Type
		}
		setOr(cfg, keyFor(cfg, "machine"), pick)
	} else {
		setOr(cfg, keyFor(cfg, "machine"), ask(in, "machine", *machine))
	}
	setOr(cfg, keyFor(cfg, "disk"), ask(in, "disk GiB (20 / 40 / 80 / 160)", strconv.Itoa(*disk)))
	setOr(cfg, "idle_timeout", ask(in, "park after idle (15m / 30m / 1h / 2h / never)", cfg.IdleTimeout))
	setOr(cfg, "unattended_cap", ask(in, "runaway cap (4h / 8h / 24h / never)", cfg.UnattendedCap))
	fmt.Println(ui.Dim.Render("    speed profiles:"))
	for _, p := range []string{"fast", "lean", "minimal"} {
		fmt.Printf("      %-8s %s\n", profileLabel(p), ui.Dim.Render(profileNote(*cfg, p, *disk)))
	}
	def := cfg.Profile()
	if def == "custom" {
		def = ""
	}
	if p := ask(in, "speed (fast / lean / minimal)", def); p != "" {
		if err := cfg.ApplyProfile(strings.ToLower(p)); err != nil {
			fmt.Println("  " + ui.Warn.Render("!") + " " + err.Error() + ui.Dim.Render(" — kept "+cfg.Profile()))
		}
	}
	if len(cfg.Secrets.Manifest) > 0 {
		fmt.Println(ui.Dim.Render("    copied into every session:"))
		for i, m := range cfg.Secrets.Manifest {
			fmt.Printf("      %s ~/%s\n", ui.Dim.Render(fmt.Sprintf("%d", i+1)), m)
		}
		if drop := ask(in, "numbers to leave out (enter keeps all)", ""); drop != "" {
			skip := map[int]bool{}
			for _, f := range strings.Fields(strings.ReplaceAll(drop, ",", " ")) {
				if n, err := strconv.Atoi(f); err == nil {
					skip[n] = true
				}
			}
			var kept []string
			for i, m := range cfg.Secrets.Manifest {
				if !skip[i+1] {
					kept = append(kept, m)
				}
			}
			cfg.Secrets.Manifest = kept
		}
	}
}

// setOr applies a value through config.Set, keeping the old one (and saying
// so) when it doesn't validate.
func setOr(cfg *config.Config, key, val string) {
	if err := config.Set(cfg, key, val); err != nil {
		fmt.Println("  " + ui.Warn.Render("!") + " " + err.Error() + ui.Dim.Render(" — kept "+config.Get(*cfg, key)))
	}
}

func keyFor(cfg *config.Config, what string) string {
	prefix := "aws."
	if cfg.Driver == "gcp-gce" {
		prefix = "gcp."
	}
	switch what {
	case "machine":
		if prefix == "gcp." {
			return "gcp.machine_type"
		}
		return "aws.instance_type"
	default:
		return prefix + "disk_gib"
	}
}

func machineNote(drv driver.Driver, machine string) string {
	for _, m := range drv.Machines(machine) {
		if m.Type == machine {
			return fmt.Sprintf("%s vCPU · %s GB · %s — resize any session later", m.CPU, m.Mem, m.Cost)
		}
	}
	return "resize any session later"
}

func diskMonthly(cfg config.Config, gib int) float64 {
	if cfg.Driver == "gcp-gce" {
		return float64(gib) * 0.10
	}
	return float64(gib) * 0.095
}

func profileLabel(p string) string {
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

// profileNote says what a speed profile does and costs while idle.
func profileNote(cfg config.Config, p string, disk int) string {
	switch p {
	case "fast":
		return fmt.Sprintf("image + 1 ready session per repo · new sessions in ~25s · ~$%.0f/mo per repo idle", diskMonthly(cfg, disk)+2)
	case "lean":
		return "image per repo · new sessions in ~1-2 min · ~$2/mo per repo idle"
	case "minimal":
		return "nothing stored · every session runs the full setup · $0 idle"
	}
	return fmt.Sprintf("%d ready session(s) per repo, bake reminders %s", cfg.Speed.ReadySessions, onOff(cfg.Speed.BakeReminders))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// manifestSummary names what's copied in, briefly: "Claude, Codex, tmux".
func manifestSummary(m []string) string {
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

// offerBake asks to bake the repo's session image now. A no is fine: the
// same suggestion comes back as a reminder after creates.
func offerBake(ctx context.Context, in *bufio.Reader, opts pier.Options, repo string) error {
	name := filepath.Base(repo)
	c, err := pier.Open(opts)
	if err != nil {
		return err
	}
	if c.Config().BakedImage(name) != "" {
		return nil
	}
	fmt.Println("\n " + ui.Accent.Render("4 · "+name))
	if _, err := os.Stat(filepath.Join(repo, ".pier", "setup.sh")); err != nil {
		fmt.Println(ui.Dim.Render("  no .pier/setup.sh yet — ask your agent to \"set this repo up for pier\" so sessions install its dependencies"))
	}
	if !yes(in, "No session image for "+name+" yet. Bake one now? New sessions then start in a minute or two instead of running the full setup", true) {
		fmt.Println(ui.Dim.Render("  later: `pier bake` in the repo"))
		return nil
	}
	// ctrl-c mid-bake must cancel the ctx so the bake's cleanup can
	// terminate the temporary instance — it has no supervisor, so a leaked
	// one never parks itself.
	bctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := c.Bake(bctx, pier.BakeRequest{RepoRoot: repo, Progress: func(e pier.Event) {
		fmt.Println(ui.Step(e.Message) + ui.Dim.Render(fmt.Sprintf("  +%s", e.Elapsed.Round(time.Second))))
	}})
	if err != nil {
		return err
	}
	fmt.Println("  "+ui.Mark(true), "baked", res.Image, "for", name)
	return nil
}

// askAWS: tool check, then profile (identity-checked immediately), region,
// instance type. Everything detected becomes the prefilled default.
func askAWS(in *bufio.Reader, cfg *config.Config) error {
	for bin, hint := range map[string]string{
		"aws":                    "brew install awscli",
		"session-manager-plugin": "brew install --cask session-manager-plugin",
	} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found — %s, then re-run `pier setup`", bin, hint)
		}
	}
	if profiles, err := exec.Command("aws", "configure", "list-profiles").Output(); err == nil {
		if p := strings.Fields(string(profiles)); len(p) > 0 {
			fmt.Println(ui.Dim.Render("  aws profiles: " + strings.Join(p, ", ")))
		}
	}
	cfg.AWS.Profile = ask(in, "AWS profile", or(cfg.AWS.Profile, "default"))
	arn, err := checkIdentity(in, cfg.AWS.Profile)
	if err != nil {
		return err
	}
	fmt.Println("  "+ui.Mark(true), "authenticated as", ui.Bold.Render(arn))
	if cfg.AWS.Region == "" {
		out, _ := exec.Command("aws", "configure", "get", "region", "--profile", cfg.AWS.Profile).Output()
		cfg.AWS.Region = strings.TrimSpace(string(out))
	}
	cfg.AWS.Region = ask(in, "region", or(cfg.AWS.Region, "eu-central-1"))
	return nil
}

// askGCP mirrors askAWS: fail fast on a dead login right after the tool
// check, then project (access-checked immediately), zone, machine type.
func askGCP(in *bufio.Reader, cfg *config.Config) error {
	if _, err := exec.LookPath("gcloud"); err != nil {
		return fmt.Errorf("gcloud not found — install the Google Cloud CLI (cloud.google.com/sdk/docs/install), then re-run `pier setup`")
	}
	acct, err := gcloudAccount()
	if err != nil {
		fmt.Println("  "+ui.Mark(false), "no active gcloud account")
		if !yes(in, "run `gcloud auth login` now?", true) {
			return fmt.Errorf("run `gcloud auth login`, then re-run `pier setup`")
		}
		login := exec.Command("gcloud", "auth", "login")
		login.Stdin, login.Stdout, login.Stderr = os.Stdin, os.Stdout, os.Stderr
		if login.Run() != nil || func() error { acct, err = gcloudAccount(); return err }() != nil {
			return fmt.Errorf("login did not stick — run `gcloud auth login`, then re-run `pier setup`")
		}
	}
	fmt.Println("  "+ui.Mark(true), "authenticated as", ui.Bold.Render(acct))

	def := cfg.GCP.Project
	if def == "" {
		// The operator's active gcloud project is only ever a prompt default;
		// pier itself always passes --project explicitly.
		out, _ := exec.Command("gcloud", "config", "get-value", "project").Output()
		if p := strings.TrimSpace(string(out)); p != "" && p != "(unset)" {
			def = p
		}
	}
	cfg.GCP.Project = ask(in, "GCP project", def)
	if cfg.GCP.Project == "" {
		return fmt.Errorf("pier needs a project — create one at console.cloud.google.com, then re-run `pier setup`")
	}
	if out, err := exec.Command("gcloud", "projects", "describe", cfg.GCP.Project,
		"--format", "value(projectId)").CombinedOutput(); err != nil {
		return fmt.Errorf("cannot access project %q: %s", cfg.GCP.Project, strings.TrimSpace(string(out)))
	}
	cfg.GCP.Zone = ask(in, "zone", cfg.GCP.Zone)
	return nil
}

// gcloudAccount is the active gcloud login, erroring when there is none.
func gcloudAccount() (string, error) {
	out, err := exec.Command("gcloud", "auth", "list",
		"--filter=status:ACTIVE", "--format=value(account)").Output()
	acct, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || acct == "" {
		return "", fmt.Errorf("no active gcloud account")
	}
	return acct, nil
}

// checkIdentity fails fast on dead credentials — right after the profile
// question, not six prompts later at groundwork. It also shows which
// account/ARN is about to be touched. Expired SSO gets the exact login
// command and an offer to run it inline.
// gitToplevel is the enclosing repo's root, "" when the wizard runs outside
// any repo (bake needs one — images are repo-specific).
func gitToplevel() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func checkIdentity(in *bufio.Reader, profile string) (string, error) {
	sts := func() (string, error) {
		// Stdin and stderr stay attached: mfa_serial profiles prompt for a
		// code here, and answering it once caches the session token for every
		// buffered call after this one. Stderr is teed into a buffer so the
		// SSO detection below can still read the failure text.
		var errb bytes.Buffer
		cmd := exec.Command("aws", "sts", "get-caller-identity",
			"--query", "Arn", "--output", "text", "--profile", profile)
		cmd.Stdin = os.Stdin
		cmd.Stderr = io.MultiWriter(os.Stderr, &errb)
		out, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(errb.String())
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("%s", msg)
		}
		return strings.TrimSpace(string(out)), nil
	}
	arn, err := sts()
	if err == nil {
		return arn, nil
	}
	if low := strings.ToLower(err.Error()); strings.Contains(low, "sso") && (strings.Contains(low, "expired") || strings.Contains(low, "refresh")) {
		fmt.Printf("  %s the SSO session for profile %q has expired\n", ui.Mark(false), profile)
		if yes(in, "run `aws sso login --profile "+profile+"` now?", true) {
			login := exec.Command("aws", "sso", "login", "--profile", profile)
			login.Stdin, login.Stdout, login.Stderr = os.Stdin, os.Stdout, os.Stderr
			if login.Run() == nil {
				return sts()
			}
		}
		return "", fmt.Errorf("profile %q needs `aws sso login --profile %s`, then re-run `pier setup`", profile, profile)
	}
	return "", fmt.Errorf("credentials for profile %q aren't working: %w", profile, err)
}

// claudeSelfContained reports whether ~/.claude/settings.json's env block
// alone authenticates claude on a fresh VM (Foundry/API-key setups — the
// file travels with the manifest). Bedrock/Vertex/Entra need cloud creds
// that deliberately never enter sessions, so those still get the token
// prompt.
func claudeSelfContained() string {
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

// detectManifest proposes $HOME-relative agent config worth carrying into
// sessions (only entries that exist).
func detectManifest() []string {
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

func ask(in *bufio.Reader, prompt, def string) string {
	if def != "" {
		fmt.Printf("  %s %s: ", prompt, ui.Dim.Render("["+def+"]"))
	} else {
		fmt.Printf("  %s: ", prompt)
	}
	line, _ := in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func yes(in *bufio.Reader, prompt string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	line := strings.ToLower(ask(in, prompt+" ["+hint+"]", ""))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

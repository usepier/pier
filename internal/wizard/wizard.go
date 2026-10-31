// Package wizard implements `pier setup`: cloud and account → every default
// shown for confirm or edit → groundwork and checks → the offer to bake the
// current repo. Everything detected becomes a prefilled default, so a second
// dev on a prepared account just presses enter a few times.
package wizard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/ui"
	"github.com/usepier/pier/pkg/pier"
)

// Run is `pier setup`: cloud and account, then every default shown in one
// block for the user to confirm or edit (nothing is applied silently), the
// account groundwork, checks, and — inside a repo — the offer to bake its
// session image. It ends by pointing at the settings page, where all of it
// can change later.
func Run(opts pier.Options, printAdminOnly bool) error {
	if printAdminOnly {
		// The doc matches the configured cloud; without a config, AWS.
		cfg, _ := config.Load()
		fmt.Print(pier.AdminDoc(cfg.Driver))
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	fmt.Println("\n " + ui.Title.Render("⚓ pier setup") +
		ui.Dim.Render("  sessions run on your own cloud account; nothing leaves it"))
	fmt.Println()
	for _, t := range pier.Tools("") {
		if !t.Found {
			return fmt.Errorf("%s not found — %s, then re-run `pier setup`", t.Name, t.Hint)
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
	cloud, err := pier.CloudDriver(ask(in, "cloud (aws / gcp)", cloudDef))
	if err != nil {
		return fmt.Errorf("unknown cloud — aws or gcp")
	}
	cfg.Driver = cloud
	if cloud == "aws-ec2" {
		if err := askAWS(in, &cfg); err != nil {
			return err
		}
		groundwork = "IAM role + instance profile + egress-only security group"
	} else {
		if err := askGCP(in, &cfg); err != nil {
			return err
		}
		groundwork = "compute + IAP APIs, IAP-only firewall rules"
	}
	drv, err := pier.NewDriver(cfg, opts)
	if err != nil {
		return err
	}

	// 2. defaults — shown together, confirmed or edited, never assumed.
	if len(cfg.Secrets.Manifest) == 0 {
		cfg.Secrets.Manifest = pier.DetectManifest()
	}
	home, _ := os.UserHomeDir()
	agents := pier.AgentDirs(home)
	skills := len(agents) > 0
	fmt.Println("\n " + ui.Accent.Render("2 · defaults") + ui.Dim.Render("  for new sessions — all of it changes later in settings"))
	printDefaults(cfg, drv, agents, skills)
	switch strings.ToLower(ask(in, "use these? [Y]es · [e]dit", "")) {
	case "e", "edit":
		editDefaults(in, &cfg, drv)
		skills = false // edited: the per-agent prompts below decide instead
	}
	if cfg.Secrets.ClaudeOAuthToken == "" {
		if p := pier.ClaudeSelfContained(); p != "" && slices.Contains(cfg.Secrets.Manifest, ".claude/settings.json") {
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
	rep, err := pier.Groundwork(ctx, cfg, opts)
	var se *pier.SetupError
	if errors.As(err, &se) && se.Kind == "no_vpc" {
		// Enterprise accounts routinely delete the default VPC. Without this
		// prompt setup would abort before writing config, and the only other
		// place to set aws.subnet is a config file that does not exist yet.
		fmt.Println("  "+ui.Mark(false), err.Error())
		if subnet := ask(in, "subnet for sessions (subnet-...)", ""); subnet != "" {
			cfg.AWS.Subnet = subnet
			if drv, err = pier.NewDriver(cfg, opts); err != nil {
				return err
			}
			rep, err = pier.Groundwork(ctx, cfg, opts)
		}
	}
	if err != nil {
		if errors.As(err, &se) {
			switch se.Kind {
			case "billing":
				// Fresh GCP projects routinely have no billing account;
				// enabling compute fails on it with a mouthful.
				return fmt.Errorf("%w\n\nthe project has no billing account — link one (console.cloud.google.com/billing), then re-run `pier setup`", err)
			case "permission":
				return fmt.Errorf("%w\n\nno admin rights? `pier setup --print-admin` prints the commands for your admin", err)
			}
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
	if err := pier.SaveSetup(cfg); err != nil {
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

	// Images already in the account come back into the config — a reset
	// config must neither orphan them nor send sessions to the stock image.
	if err := adoptImages(ctx, opts); err != nil {
		return err
	}

	// 4. this repo's session image: required when the repo has a bake hook,
	// offered otherwise.
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
	for _, r := range pier.DefaultRows(cfg, drv, agents, skills) {
		fmt.Printf("    %-14s %s %s\n", r.Label, r.Value, ui.Dim.Render(r.Note))
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
		setOr(cfg, pier.MachineKey(*cfg), pick)
	} else {
		setOr(cfg, pier.MachineKey(*cfg), ask(in, "machine", *machine))
	}
	setOr(cfg, pier.DiskKey(*cfg), ask(in, "disk GiB (20 / 40 / 80 / 160)", strconv.Itoa(*disk)))
	setOr(cfg, "idle_timeout", ask(in, "park after idle (15m / 30m / 1h / 2h / never)", cfg.IdleTimeout))
	setOr(cfg, "unattended_cap", ask(in, "runaway cap (4h / 8h / 24h / never)", cfg.UnattendedCap))
	fmt.Println(ui.Dim.Render("    speed profiles:"))
	for _, p := range []string{"fast", "lean", "minimal"} {
		fmt.Printf("      %-8s %s\n", pier.ProfileLabel(p), ui.Dim.Render(pier.ProfileNote(*cfg, p, *disk)))
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

// adoptImages records the account's existing session images and deletes
// superseded ones.
func adoptImages(ctx context.Context, opts pier.Options) error {
	c, err := pier.Open(opts)
	if err != nil {
		return err
	}
	rep, err := c.ReconcileImages(ctx)
	for repo, img := range rep.Adopted {
		fmt.Println("  " + ui.OK.Render("=") + " found " + repo + "'s session image " + img)
	}
	for _, img := range rep.Deleted {
		fmt.Println("  " + ui.OK.Render("-") + " deleted superseded image " + img)
	}
	return err
}

// offerBake bakes the repo's session image now. A repo with .pier/bake.sh
// doesn't work without one, so it bakes without asking; for any other repo
// a no is fine — the suggestion comes back as a reminder after creates.
func offerBake(ctx context.Context, in *bufio.Reader, opts pier.Options, repo string) error {
	name := filepath.Base(repo)
	c, err := pier.Open(opts)
	if err != nil {
		return err
	}
	if c.Config().BakedImage(name) != "" {
		// Already baked: make sure its pool is waiting for the first session.
		if c.Config().PoolSize(name) > 0 {
			printFill(c.SpawnFill(repo))
		}
		return nil
	}
	fmt.Println("\n " + ui.Accent.Render("4 · "+name))
	base := payload.OriginBase(context.Background(), repo)
	if payload.FileAt(repo, base, ".pier/setup.sh") == nil {
		fmt.Println(ui.Dim.Render("  no .pier/setup.sh yet — ask your agent to \"set this repo up for pier\" so sessions install its dependencies"))
	}
	if payload.BakeHook(repo, base) != nil {
		fmt.Println("  No session image for " + name + " yet, and its .pier/bake.sh toolchains are required — baking one now.")
	} else if !yes(in, "No session image for "+name+" yet. Bake one now? New sessions then start in a minute or two instead of running the full setup", true) {
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
	fmt.Println("  "+ui.Mark(true), "baked", name, "— image", res.Image, "is being saved in the background")
	if res.FinishLog != "" {
		fmt.Println(ui.Dim.Render("  sessions start from it once it's saved, and the pool fills from it then — log: " + ui.Tilde(res.FinishLog)))
	}
	return nil
}

// printFill reports a background pool fill starting.
func printFill(log string, err error) {
	if err != nil {
		fmt.Println("  " + ui.Warn.Render("!") + " pool fill didn't start: " + err.Error() + " — run `pier pool fill` in the repo")
		return
	}
	fmt.Println(ui.Dim.Render("  filling the pool in the background, so the first session starts set up — log: " + ui.Tilde(log)))
}

// askAWS: tool check, then profile (identity-checked immediately), region,
// instance type. Everything detected becomes the prefilled default.
func askAWS(in *bufio.Reader, cfg *config.Config) error {
	for _, t := range pier.Tools("aws-ec2") {
		if !t.Found && (t.Name == "aws" || t.Name == "session-manager-plugin") {
			return fmt.Errorf("%s not found — %s, then re-run `pier setup`", t.Name, t.Hint)
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
	acct, err := pier.GCloudAccount()
	if err != nil {
		fmt.Println("  "+ui.Mark(false), "no active gcloud account")
		if !yes(in, "run `gcloud auth login` now?", true) {
			return fmt.Errorf("run `gcloud auth login`, then re-run `pier setup`")
		}
		login := exec.Command("gcloud", "auth", "login")
		login.Stdin, login.Stdout, login.Stderr = os.Stdin, os.Stdout, os.Stderr
		if login.Run() != nil || func() error { acct, err = pier.GCloudAccount(); return err }() != nil {
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
	if err := pier.CheckGCPProject(cfg.GCP.Project); err != nil {
		return err
	}
	cfg.GCP.Zone = ask(in, "zone", cfg.GCP.Zone)
	return nil
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
	if pier.SSOExpired(err.Error()) {
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

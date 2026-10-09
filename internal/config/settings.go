package config

// The settings pier's frontends edit: each field's schema (label, help,
// choices, the cloud it applies to), in page order, and reading and
// writing them by key with validation.

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// FieldKind tells the settings UI how to edit a field.
type FieldKind int

const (
	KindText    FieldKind = iota // free-text editor
	KindChoice                   // pick from the fixed Options list
	KindMachine                  // pick from the driver's machine catalog (injected)
)

// Option is one choice in a KindChoice picker. Value is what Set receives;
// Label is what the picker shows (falls back to Value); Desc is a dim
// right-column annotation (a human region name, machine specs, guidance).
type Option struct {
	Value string
	Label string
	Desc  string
}

// Field is one editable setting: how it's grouped, labeled, and explained,
// how it's edited (Kind + Options), and how its value reads back (Empty /
// Suffix). Validation always runs through Set, the single write path.
type Field struct {
	Key      string
	Group    string // a Groups key
	Cloud    string // "aws" / "gcp": applies only while that cloud is active; "" = always
	Label    string // human label shown on the row
	Hint     string // short one-liner beside the value
	Detail   string // multi-line (\n-split) footer for the selected field
	Default  string // shown in the footer; "" prints nothing
	Kind     FieldKind
	Options  []Option // KindChoice choices; nil otherwise
	NoCustom bool     // a closed enum: the picker offers no "custom…" escape
	Empty    string   // placeholder for an empty value ("" → "(unset)")
	Suffix   string   // appended to a raw value on display (e.g. " GiB")
}

// Display renders a stored value for humans: choice labels instead of raw
// values, a placeholder when empty, a unit suffix otherwise. Storage is
// untouched — this is presentation only.
func (f Field) Display(v string) string {
	if v == "" {
		if f.Empty != "" {
			return f.Empty
		}
		return "(unset)"
	}
	if f.Kind == KindChoice {
		for _, o := range f.Options {
			if o.Value == v {
				if o.Label != "" {
					return o.Label
				}
				return o.Value
			}
		}
	}
	return v + f.Suffix
}

// durationOpts / diskOpts are shared so the aws and gcp groups read the same.
var (
	idleOpts = []Option{{Value: "15m"}, {Value: "30m"}, {Value: "1h"}, {Value: "2h"}, {Value: "never"}}
	capOpts  = []Option{{Value: "4h"}, {Value: "8h"}, {Value: "24h"}, {Value: "never"}}
	diskOpts = []Option{{Value: "20", Label: "20 GiB"}, {Value: "40", Label: "40 GiB"}, {Value: "80", Label: "80 GiB"}, {Value: "160", Label: "160 GiB"}}
)

// Themes are the accent color names, in picker order. The colors live with
// the UI (internal/ui); config only validates the name.
var Themes = []string{"teal", "navy", "violet", "emerald", "orange", "crimson", "pink", "amber", "graphite"}

func themeOptions() []Option {
	opts := make([]Option, len(Themes))
	for i, t := range Themes {
		opts[i] = Option{Value: t}
	}
	return opts
}

// Groups orders the settings page's sections. Every field belongs to one.
var Groups = []struct{ Key, Title string }{
	{"cloud", "Cloud"},
	{"sessions", "New sessions"},
	{"idle", "Idle & cost"},
	{"agents", "Agents"},
	{"speed", "Speed"},
	{"app", "Appearance"},
}

// Settings lists the settable fields in display order, grouped by what the
// user is thinking about when they open the page. Fields tagged with a Cloud
// only apply (and only show) while that cloud is active. Secrets and baked
// images are absent on purpose: `pier setup` and `pier bake` manage them.
var Settings = []Field{
	{
		Key: "driver", Group: "cloud", Label: "provider", Hint: "where sessions run",
		Kind: KindChoice, NoCustom: true, Default: "aws-ec2",
		Options: []Option{
			{Value: "aws-ec2", Label: "AWS", Desc: "Amazon EC2 · direct ssh or SSM"},
			{Value: "gcp-gce", Label: "GCP", Desc: "Google Compute Engine · IAP tunnel"},
		},
		Detail: "Which provider runs new sessions.\nExisting sessions stay on the cloud they were built on. Switching shows that cloud's settings here; run `pier doctor` afterwards to check its groundwork.",
	},
	{
		Key: "aws.profile", Group: "cloud", Cloud: "aws", Label: "CLI profile", Hint: "AWS CLI profile pier uses",
		Kind: KindText, Empty: "(default)",
		Detail: "The AWS CLI profile every pier command runs under — SSO, MFA and credentials all come from it.\nset one up with `aws configure`.",
	},
	{
		Key: "aws.region", Group: "cloud", Cloud: "aws", Label: "region", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "eu-central-1", Empty: "from the CLI profile",
		Options: []Option{
			{Value: "us-east-1", Desc: "N. Virginia"},
			{Value: "us-east-2", Desc: "Ohio"},
			{Value: "us-west-2", Desc: "Oregon"},
			{Value: "eu-west-1", Desc: "Ireland"},
			{Value: "eu-central-1", Desc: "Frankfurt"},
			{Value: "ap-south-1", Desc: "Mumbai"},
			{Value: "ap-southeast-1", Desc: "Singapore"},
			{Value: "ap-northeast-1", Desc: "Tokyo"},
		},
		Detail: "New session VMs launch here; existing sessions stay where they are.\nAfter switching, `pier doctor` checks the groundwork exists in the new region. Session images are per region: rebake there.",
	},
	{
		Key: "gcp.project", Group: "cloud", Cloud: "gcp", Label: "project", Hint: "project sessions live in",
		Kind:   KindText,
		Detail: "Sessions are created in this project — pier always passes it explicitly, never your active gcloud default. Required on GCP.",
	},
	{
		Key: "gcp.zone", Group: "cloud", Cloud: "gcp", Label: "zone", Hint: "where new VMs launch",
		Kind: KindChoice, Default: "europe-west3-a",
		Options: []Option{
			{Value: "us-central1-a", Desc: "Iowa"},
			{Value: "us-east1-b", Desc: "S. Carolina"},
			{Value: "europe-west1-b", Desc: "Belgium"},
			{Value: "europe-west3-a", Desc: "Frankfurt"},
			{Value: "europe-west4-a", Desc: "Netherlands"},
			{Value: "asia-southeast1-a", Desc: "Singapore"},
		},
		Detail: "New session VMs launch in this zone; existing sessions stay put.",
	},
	{
		Key: "aws.direct", Group: "cloud", Cloud: "aws", Label: "connection", Hint: "direct is fastest; SSM if port 22 is blocked",
		Kind: KindChoice, NoCustom: true, Default: "direct ssh",
		Options: []Option{
			{Value: "true", Label: "direct ssh", Desc: "straight to the VM's public IP — full speed"},
			{Value: "false", Label: "SSM tunnel", Desc: "everything through SSM (~1 MB/s)"},
		},
		Detail: "How your terminal reaches the VM.\ndirect ssh dials the public IP at full speed, opens TCP 22 to your current IP only, and falls back to the SSM tunnel by itself when that's blocked.\nSSM tunnel forces the tunnel — for networks that block outbound 22 or orgs that disallow the ingress rule.",
	},
	{
		Key: "aws.subnet", Group: "cloud", Cloud: "aws", Label: "subnet", Hint: "only needed without a default VPC",
		Kind: KindText, Empty: "default VPC",
		Detail: "Only for accounts whose default VPC was deleted: session VMs launch in this subnet. Leave empty otherwise.\nformat: subnet-0abc123…",
	},
	{
		Key: "aws.instance_type", Group: "sessions", Cloud: "aws", Label: "machine", Hint: "VM for new sessions",
		Kind: KindMachine, Default: "t4g.medium",
		Detail: "New sessions start on this VM type. Undersize freely — `m` on the Sessions tab resizes a live session in about a minute, disk intact.",
	},
	{
		Key: "gcp.machine_type", Group: "sessions", Cloud: "gcp", Label: "machine", Hint: "VM for new sessions",
		Kind: KindMachine, Default: "e2-medium",
		Detail: "New sessions start on this machine type. Undersize freely — `m` on the Sessions tab resizes a live session in a couple of minutes, disk intact.",
	},
	{
		Key: "aws.disk_gib", Group: "sessions", Cloud: "aws", Label: "disk", Hint: "per-session disk; survives parking",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Root disk for each new session. It's what survives parking, and what a parked session costs: about $0.08-0.10 per GiB-month (40 GiB ≈ $3-4/mo).\nWith a prebuilt image, leave room for dependencies and container images.",
	},
	{
		Key: "gcp.disk_gib", Group: "sessions", Cloud: "gcp", Label: "disk", Hint: "per-session disk; survives parking",
		Kind: KindChoice, Options: diskOpts, Suffix: " GiB", Default: "40",
		Detail: "Boot disk for each new session. It's what survives parking, and what a parked session costs (40 GiB ≈ $3-4/mo).",
	},
	{
		Key: "idle_timeout", Group: "idle", Label: "park after", Hint: "park when detached and quiet",
		Kind: KindChoice, Options: idleOpts, Default: "30m",
		Detail: "A session you've detached from that goes quiet this long parks itself: the VM stops and only its disk costs money. Attaching resumes it in ~20-60s with your tmux windows and agent conversations restored.\n`pier keep` exempts one session; --idle overrides one create.",
	},
	{
		Key: "unattended_cap", Group: "idle", Label: "runaway cap", Hint: "park even while the agent keeps working",
		Kind: KindChoice, Options: capOpts, Default: "8h",
		Detail: "Parks a session even while the agent is busy, once you've been detached this long — a looping agent can't burn compute for days.\n--cap overrides one create.",
	},
	{
		Key: "agents.default", Group: "agents", Label: "agent", Hint: "who new chats talk to",
		Kind: KindChoice, NoCustom: true, Default: "claude",
		Options: []Option{
			{Value: "claude", Label: "Claude", Desc: "Claude Code"},
			{Value: "codex", Label: "Codex", Desc: "OpenAI Codex"},
		},
		Detail: "New chats — in the app, and `pier <branch> -p` on the command line — start with this agent unless you pick another.",
	},
	{
		Key: "agents.claude_model", Group: "agents", Label: "Claude model", Hint: "model new Claude chats run",
		Kind: KindChoice, Empty: "account default",
		Options: []Option{
			{Value: "opus", Label: "Opus", Desc: "for complex work"},
			{Value: "sonnet", Label: "Sonnet", Desc: "efficient for most tasks"},
			{Value: "haiku", Label: "Haiku", Desc: "fastest"},
		},
		Detail: "Any model name claude takes (--model). Empty: what your account defaults to. A chat can switch models as it goes.",
	},
	{
		Key: "agents.claude_effort", Group: "agents", Label: "Claude effort", Hint: "how hard it thinks",
		Kind: KindChoice, NoCustom: true, Empty: "model default",
		Options: []Option{
			{Value: "", Label: "model default"},
			{Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"}, {Value: "max"},
		},
		Detail: "More effort thinks longer before acting: slower and costlier, better on hard problems.",
	},
	{
		Key: "agents.claude_mode", Group: "agents", Label: "Claude permissions", Hint: "what it does without asking",
		Kind: KindChoice, NoCustom: true, Default: "default",
		Options: []Option{
			{Value: "default", Label: "ask", Desc: "asks before edits and commands it isn't sure are safe"},
			{Value: "acceptEdits", Label: "accept edits", Desc: "edits without asking, asks before commands"},
			{Value: "plan", Label: "plan", Desc: "plans first, changes nothing until you approve"},
			{Value: "bypassPermissions", Label: "bypass", Desc: "runs everything without asking"},
		},
		Detail: "Sessions are machines of their own, so a careless command can't reach your laptop — but it can still push, publish or delete within the session.",
	},
	{
		Key: "agents.codex_model", Group: "agents", Label: "Codex model", Hint: "model new Codex chats run",
		Kind: KindText, Empty: "account default",
		Detail: "Any model name codex takes (-m). Empty: what your account defaults to.",
	},
	{
		Key: "agents.codex_effort", Group: "agents", Label: "Codex effort", Hint: "how hard it reasons",
		Kind: KindChoice, NoCustom: true, Empty: "model default",
		Options: []Option{
			{Value: "", Label: "model default"},
			{Value: "minimal"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"},
		},
		Detail: "More effort reasons longer before acting.",
	},
	{
		Key: "agents.codex_mode", Group: "agents", Label: "Codex permissions", Hint: "what it does without asking",
		Kind: KindChoice, NoCustom: true, Default: "default",
		Options: []Option{
			{Value: "readOnly", Label: "read only", Desc: "reads and answers, asks before changing anything"},
			{Value: "default", Label: "auto", Desc: "works in the repo and the network, asks before anything else"},
			{Value: "plan", Label: "plan", Desc: "plans first, changes nothing until you approve"},
			{Value: "bypassPermissions", Label: "full access", Desc: "runs everything without asking"},
		},
	},
	{
		Key: "speed.profile", Group: "speed", Label: "profile", Hint: "preset for the two rows below",
		Kind: KindChoice, NoCustom: true, Default: "fast",
		Options: []Option{
			{Value: "fast", Label: "Fast", Desc: "image + a pool of 1 per repo · ~25s starts"},
			{Value: "lean", Label: "Lean", Desc: "image only · ~1-2 min starts, image storage only"},
			{Value: "minimal", Label: "Minimal", Desc: "nothing stored · full setup every start, $0 idle"},
		},
		Detail: "A profile is a preset for bake reminders and pool size. Change either row and the profile reads custom.\nFast: new sessions claim a parked, set-up session from the pool (~25s); each pooled session costs its parked disk.\nLean: new sessions boot from the repo's image and re-run setup warm.\nMinimal: nothing is stored; every session runs the full setup.",
	},
	{
		Key: "speed.pool_size", Group: "speed", Label: "pool size", Hint: "parked, set-up sessions per baked repo",
		Kind: KindChoice, NoCustom: true, Default: "1",
		Options: []Option{
			{Value: "0", Label: "none"},
			{Value: "1", Desc: "the next session starts in ~25s"},
			{Value: "2", Desc: "two quick starts back to back"},
			{Value: "3"},
		},
		Detail: "How many parked, setup-complete sessions pier keeps in each baked repo's pool. A new session claims one instead of launching, so it's usable in ~25s with services up. They're stopped VMs: each costs only its disk while waiting, and runs only while being filled.\nOverride one repo on the Repos tab or with `pier pool set <n>`.",
	},
	{
		Key: "speed.bake_reminders", Group: "speed", Label: "bake reminders", Hint: "suggest `pier bake` when it would help",
		Kind: KindChoice, NoCustom: true, Default: "on",
		Options: []Option{
			{Value: "true", Label: "on", Desc: "when a repo has no image, it gets old, or .pier scripts changed"},
			{Value: "false", Label: "off"},
		},
		Detail: "pier never rebakes on its own. With reminders on it points out when a bake would make sessions start faster.",
	},
	{
		Key: "speed.image_repo", Group: "speed", Label: "image contents", Hint: "what `pier bake` puts in a repo's image",
		Kind: KindChoice, NoCustom: true, Default: "toolchains + repo",
		Options: []Option{
			{Value: "true", Label: "toolchains + repo", Desc: "checkout with setup already run — fastest starts"},
			{Value: "false", Label: "toolchains only", Desc: "keeps repo state out of images"},
		},
		Detail: "toolchains + repo: the bake runs .pier/setup.sh on a checkout, scrubs every secret pier pushed, and images the result; sessions only fetch what changed and re-run setup warm.\ntoolchains only: images carry the harnesses and .pier/bake.sh tools; every session runs the full setup.\nApplies to the next `pier bake`.",
	},
	{
		Key: "speed.auto_refill", Group: "speed", Label: "refill pools", Hint: "rebuild a pool that runs low on its own",
		Kind: KindChoice, NoCustom: true, Default: "off",
		Options: []Option{
			{Value: "true", Label: "on", Desc: "keep every pool full in the background"},
			{Value: "false", Label: "off", Desc: "refill after a claim, a bake, or `pier pool fill`"},
		},
		Detail: "On: whenever a repo's pool is below its size — one was taken, members aged out, or settings changed — pier builds pooled sessions in the background while the Mac app runs. Each costs its parked disk while it waits, and a few minutes of machine time to build.\nOnly repos linked to a clone on this Mac are filled; a pool is built from your clone.",
	},
	{
		Key: "speed.reminder_age", Group: "speed", Label: "rebake after", Hint: "remind when an image gets this old",
		Kind: KindChoice, Default: "30d",
		Options: []Option{{Value: "14d"}, {Value: "30d"}, {Value: "60d"}, {Value: "never"}},
		Detail:  "Once a repo's image is this old, pier suggests a rebake so new sessions start with current dependencies.",
	},
	{
		Key: "theme", Group: "app", Label: "accent color", Hint: "the app's highlight color",
		Kind: KindChoice, NoCustom: true, Default: "teal", Empty: "teal",
		Options: themeOptions(),
		Detail:  "The color pier uses for its borders, tabs, keys and highlights, in the app and the CLI.",
	},
}

// Get returns the current value of a settable key ("" for unknown keys).
func Get(c Config, key string) string {
	switch key {
	case "driver":
		return c.Driver
	case "idle_timeout":
		return c.IdleTimeout
	case "unattended_cap":
		return c.UnattendedCap
	case "aws.profile":
		return c.AWS.Profile
	case "aws.region":
		return c.AWS.Region
	case "aws.instance_type":
		return c.AWS.InstanceType
	case "aws.disk_gib":
		return strconv.Itoa(c.AWS.DiskGiB)
	case "aws.subnet":
		return c.AWS.Subnet
	case "aws.direct":
		return strconv.FormatBool(c.AWS.Direct)
	case "gcp.project":
		return c.GCP.Project
	case "gcp.zone":
		return c.GCP.Zone
	case "gcp.machine_type":
		return c.GCP.MachineType
	case "gcp.disk_gib":
		return strconv.Itoa(c.GCP.DiskGiB)
	case "theme":
		if c.Theme == "" {
			return "teal"
		}
		return c.Theme
	case "speed.profile":
		return c.Profile()
	case "speed.pool_size":
		return strconv.Itoa(c.Speed.PoolSize)
	case "speed.bake_reminders":
		return strconv.FormatBool(c.Speed.BakeReminders)
	case "speed.image_repo":
		return strconv.FormatBool(c.Speed.ImageRepo)
	case "speed.auto_refill":
		return strconv.FormatBool(c.Speed.AutoRefill)
	case "speed.reminder_age":
		return c.Speed.ReminderAge
	case "agents.default":
		return c.Agents.Agent()
	case "agents.claude_model":
		return c.Agents.ClaudeModel
	case "agents.claude_effort":
		return c.Agents.ClaudeEffort
	case "agents.claude_mode":
		if c.Agents.ClaudeMode == "" {
			return "default"
		}
		return c.Agents.ClaudeMode
	case "agents.codex_model":
		return c.Agents.CodexModel
	case "agents.codex_effort":
		return c.Agents.CodexEffort
	case "agents.codex_mode":
		if c.Agents.CodexMode == "" {
			return "default"
		}
		return c.Agents.CodexMode
	}
	return ""
}

// Visible reports whether a field applies under the config's active cloud.
func (c Config) Visible(f Field) bool {
	switch f.Cloud {
	case "aws":
		return !c.gcp()
	case "gcp":
		return c.gcp()
	}
	return true
}

// Set mutates one whitelisted scalar, validating it before it lands. A
// rejected value never touches the config, so the caller can keep the field
// open for a fix. Changes apply to new sessions only.
func Set(c *Config, key, val string) error {
	switch key {
	case "driver":
		// Strict: a typo like "gcp" used to save and then silently fall back to
		// the AWS driver at create time. Common aliases normalize; the rest is
		// a hard error here, not a surprise minutes later.
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "aws-ec2", "aws", "ec2":
			c.Driver = "aws-ec2"
		case "gcp-gce", "gcp", "gce", "google":
			c.Driver = "gcp-gce"
		default:
			return fmt.Errorf("driver: want aws-ec2 or gcp-gce (got %q)", val)
		}
	case "idle_timeout", "unattended_cap":
		d, err := ParkDuration(val)
		if err != nil {
			return fmt.Errorf("%s: %v (want a duration like 30m or 8h, or never)", key, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: must not be negative (got %q)", key, val)
		}
		if key == "idle_timeout" {
			c.IdleTimeout = val
		} else {
			c.UnattendedCap = val
		}
	case "aws.profile":
		c.AWS.Profile = strings.TrimSpace(val)
	case "aws.region":
		val = strings.TrimSpace(val)
		if !validRegionish(val) {
			return fmt.Errorf("aws.region: want a region like us-east-1 or eu-central-1 (got %q)", val)
		}
		c.AWS.Region = val
	case "aws.instance_type":
		val = strings.TrimSpace(val)
		if !validAWSType(val) {
			return fmt.Errorf("aws.instance_type: want a type like t4g.medium (got %q)", val)
		}
		c.AWS.InstanceType = val
	case "aws.disk_gib":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 8 {
			return fmt.Errorf("aws.disk_gib: want a whole number of GiB, at least 8 (got %q)", val)
		}
		c.AWS.DiskGiB = n
	case "aws.subnet":
		val = strings.TrimSpace(val)
		if val != "" && !strings.HasPrefix(val, "subnet-") {
			return fmt.Errorf("aws.subnet: want a subnet id like subnet-0abc123, or empty for the default VPC (got %q)", val)
		}
		c.AWS.Subnet = val
	case "gcp.project":
		val = strings.TrimSpace(val)
		if val != "" && !validProjectID(val) {
			return fmt.Errorf("gcp.project: want a project id — lowercase letters, digits, hyphens, 6-30 chars (got %q)", val)
		}
		c.GCP.Project = val
	case "gcp.zone":
		val = strings.TrimSpace(val)
		if !validRegionish(val) {
			return fmt.Errorf("gcp.zone: want a zone like europe-west3-a or us-central1-a (got %q)", val)
		}
		c.GCP.Zone = val
	case "gcp.machine_type":
		val = strings.TrimSpace(val)
		if !validGCPType(val) {
			return fmt.Errorf("gcp.machine_type: want a type like e2-medium or e2-standard-4 (got %q)", val)
		}
		c.GCP.MachineType = val
	case "gcp.disk_gib":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 10 {
			return fmt.Errorf("gcp.disk_gib: want a whole number of GiB, at least 10 (got %q)", val)
		}
		c.GCP.DiskGiB = n
	case "aws.direct":
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "on":
			c.AWS.Direct = true
		case "false", "no", "off":
			c.AWS.Direct = false
		default:
			return fmt.Errorf("aws.direct: want true or false (got %q)", val)
		}
	case "theme":
		v := strings.ToLower(strings.TrimSpace(val))
		if !slices.Contains(Themes, v) {
			return fmt.Errorf("theme: want one of %s (got %q)", strings.Join(Themes, ", "), val)
		}
		c.Theme = v
	case "speed.profile":
		return c.ApplyProfile(strings.ToLower(strings.TrimSpace(val)))
	case "speed.pool_size":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n < 0 || n > 8 {
			return fmt.Errorf("speed.pool_size: want 0-8 (got %q)", val)
		}
		c.Speed.PoolSize = n
	case "speed.bake_reminders", "speed.image_repo", "speed.auto_refill":
		var b bool
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "yes", "on":
			b = true
		case "false", "no", "off":
		default:
			return fmt.Errorf("%s: want on or off (got %q)", key, val)
		}
		switch key {
		case "speed.bake_reminders":
			c.Speed.BakeReminders = b
		case "speed.image_repo":
			c.Speed.ImageRepo = b
		default:
			c.Speed.AutoRefill = b
		}
	case "speed.reminder_age":
		val = strings.TrimSpace(val)
		if _, err := parseDays(val); err != nil {
			return fmt.Errorf("speed.reminder_age: %v, or never", err)
		}
		c.Speed.ReminderAge = val
	case "agents.default":
		switch val = strings.ToLower(strings.TrimSpace(val)); val {
		case "claude", "codex":
			c.Agents.Default = val
		default:
			return fmt.Errorf("agents.default: want claude or codex (got %q)", val)
		}
	case "agents.claude_model", "agents.codex_model":
		val = strings.TrimSpace(val)
		if strings.ContainsAny(val, " \t\n'\"`$;|&") {
			return fmt.Errorf("%s: a model name has no spaces or shell characters (got %q)", key, val)
		}
		if key == "agents.claude_model" {
			c.Agents.ClaudeModel = val
		} else {
			c.Agents.CodexModel = val
		}
	case "agents.claude_effort", "agents.codex_effort":
		val = strings.TrimSpace(val)
		// Agents add effort levels with their models: any plain word.
		if val != "" && !effortWord.MatchString(val) {
			return fmt.Errorf("%s: an effort is a word like low, high or max (got %q)", key, val)
		}
		if key == "agents.claude_effort" {
			c.Agents.ClaudeEffort = val
		} else {
			c.Agents.CodexEffort = val
		}
	case "agents.claude_mode", "agents.codex_mode":
		val = strings.TrimSpace(val)
		modes := map[string]bool{"default": true, "plan": true, "bypassPermissions": true, "acceptEdits": key == "agents.claude_mode", "readOnly": key == "agents.codex_mode"}
		if !modes[val] {
			return fmt.Errorf("%s: not a permission mode this agent has (got %q)", key, val)
		}
		if key == "agents.claude_mode" {
			c.Agents.ClaudeMode = val
		} else {
			c.Agents.CodexMode = val
		}
	default:
		keys := make([]string, len(Settings))
		for i, s := range Settings {
			keys[i] = s.Key
		}
		return fmt.Errorf("unknown key %q — settable: %s", key, strings.Join(keys, ", "))
	}
	return nil
}

var effortWord = regexp.MustCompile(`^[a-z]{2,16}$`)

// validRegionish is a lenient guard for AWS regions and GCP zones: a lowercase
// token with a hyphen and a digit (us-east-1, europe-west3-a, us-gov-west-1).
// Deliberately loose — it catches spaces, capitals, and wrong-field typos
// without rejecting valid or future names the strict picker doesn't list.
func validRegionish(s string) bool {
	if s == "" || !strings.Contains(s, "-") {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '-':
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasDigit
}

// validAWSType matches a family.size token (t4g.medium, m7g.4xlarge).
func validAWSType(s string) bool {
	fam, size, ok := strings.Cut(s, ".")
	return ok && lowerAlnum(fam) && lowerAlnum(size)
}

// validGCPType matches a hyphenated machine type (e2-medium, e2-standard-4,
// e2-custom-4-8192).
func validGCPType(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' || !strings.Contains(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// validProjectID is the shape of a GCP project id: 6-30 chars, starts with a
// letter, lowercase letters/digits/hyphens, no trailing hyphen.
func validProjectID(s string) bool {
	if len(s) < 6 || len(s) > 30 || s[0] < 'a' || s[0] > 'z' || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func lowerAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

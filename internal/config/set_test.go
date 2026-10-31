package config

import (
	"os"
	"strings"
	"testing"
)

func TestSet(t *testing.T) {
	cfg := Default()

	if err := Set(&cfg, "aws.instance_type", "t4g.xlarge"); err != nil {
		t.Fatal(err)
	}
	if cfg.AWS.InstanceType != "t4g.xlarge" {
		t.Errorf("instance_type = %q", cfg.AWS.InstanceType)
	}

	if err := Set(&cfg, "idle_timeout", "never"); err != nil {
		t.Fatal(err)
	}
	if err := Set(&cfg, "idle_timeout", "not-a-duration"); err == nil {
		t.Error("bad duration must not apply")
	}

	if err := Set(&cfg, "aws.disk_gib", "7"); err == nil {
		t.Error("disk below 8 GiB must not apply")
	}
	if err := Set(&cfg, "aws.disk_gib", "80"); err != nil {
		t.Fatal(err)
	}
	if cfg.AWS.DiskGiB != 80 {
		t.Errorf("disk_gib = %d", cfg.AWS.DiskGiB)
	}

	// Direct connect is the default; false is the opt-out to the SSM tunnel.
	if !cfg.AWS.Direct {
		t.Error("aws.direct must default to true")
	}
	if err := Set(&cfg, "aws.direct", "false"); err != nil {
		t.Fatal(err)
	}
	if cfg.AWS.Direct {
		t.Error("aws.direct = false must apply")
	}
	if Get(cfg, "aws.direct") != "false" {
		t.Errorf("aws.direct reads back %q", Get(cfg, "aws.direct"))
	}
	if err := Set(&cfg, "aws.direct", "maybe"); err == nil {
		t.Error("aws.direct must reject non-boolean values")
	}
	if err := Set(&cfg, "aws.direct", "on"); err != nil {
		t.Fatal(err)
	}
	if !cfg.AWS.Direct {
		t.Error("aws.direct = on must apply")
	}

	// Secrets stay out of the set path: the error must name the valid keys.
	err := Set(&cfg, "secrets.claude_oauth_token", "x")
	if err == nil || !strings.Contains(err.Error(), "settable:") {
		t.Errorf("secret key must be rejected with the settable list, got %v", err)
	}

	if err := Set(&cfg, "gcp.disk_gib", "9"); err == nil {
		t.Error("gcp disk below 10 GiB must not apply")
	}
	if err := Set(&cfg, "gcp.project", "my-project"); err != nil {
		t.Fatal(err)
	}
	if Get(cfg, "gcp.project") != "my-project" {
		t.Errorf("gcp.project reads back %q", Get(cfg, "gcp.project"))
	}

	// Every advertised setting must round-trip through Get. Keys with no
	// sensible default (credentials, org-specific ids) are empty until setup;
	// agent models and efforts are empty for "what the agent defaults to".
	emptyOK := map[string]bool{"aws.profile": true, "aws.region": true, "aws.subnet": true, "gcp.project": true,
		"agents.model": true, "agents.effort": true}
	for _, s := range Settings {
		if Get(cfg, s.Key) == "" && !emptyOK[s.Key] {
			t.Errorf("Get(%q) is empty on a default config", s.Key)
		}
	}
}

func TestAgentSettings(t *testing.T) {
	cfg := Default()
	for k, v := range map[string]string{"agents.default": "codex", "agents.model": "gpt-6-astra", "agents.effort": "minimal",
		"agents.claude_mode": "acceptEdits", "agents.codex_mode": "readOnly"} {
		if err := Set(&cfg, k, v); err != nil {
			t.Fatalf("%s=%s: %v", k, v, err)
		}
	}
	if cfg.Agents.Agent() != "codex" {
		t.Error("default agent")
	}
	// One default model: the other agent runs its own default.
	if m, e, mode := cfg.Agents.For("codex"); m != "gpt-6-astra" || e != "minimal" || mode != "readOnly" {
		t.Errorf("codex defaults = %s %s %s", m, e, mode)
	}
	if m, e, mode := cfg.Agents.For("claude"); m != "" || e != "" || mode != "acceptEdits" {
		t.Errorf("claude defaults = %s %s %s", m, e, mode)
	}
	for k, v := range map[string]string{"agents.default": "gemini", "agents.codex_mode": "acceptEdits", "agents.claude_mode": "readOnly",
		"agents.effort": "very high", "agents.model": "opus; rm -rf /"} {
		if err := Set(&cfg, k, v); err == nil {
			t.Errorf("%s=%s was accepted", k, v)
		}
	}
}

// TestSetValidation covers the hardened validators added with the settings
// redesign: a bad value must be rejected here, at Set, not silently swallowed
// to fail minutes later at create time.
func TestSetValidation(t *testing.T) {
	cfg := Default()

	// driver used to accept anything and then fall back to AWS. Aliases
	// normalize; garbage is a hard error.
	if err := Set(&cfg, "driver", "gcp"); err != nil || cfg.Driver != "gcp-gce" {
		t.Errorf("driver alias gcp must normalize to gcp-gce, got %q err=%v", cfg.Driver, err)
	}
	if err := Set(&cfg, "driver", "azure"); err == nil {
		t.Error("an unknown driver must be rejected, not saved and silently downgraded")
	}
	cfg.Driver = "aws-ec2"

	// A negative duration parses but is nonsense.
	if err := Set(&cfg, "idle_timeout", "-5m"); err == nil {
		t.Error("a negative idle_timeout must be rejected")
	}

	for _, c := range []struct {
		key, val string
		ok       bool
	}{
		{"aws.region", "eu-central-1", true},
		{"aws.region", "US-EAST-1", false}, // uppercase
		{"aws.region", "us east 1", false}, // spaces
		{"aws.region", "region", false},    // no digit
		{"aws.instance_type", "m7g.4xlarge", true},
		{"aws.instance_type", "not a type", false},
		{"aws.subnet", "subnet-0abc123", true},
		{"aws.subnet", "", true}, // empty = default VPC
		{"aws.subnet", "vpc-123", false},
		{"gcp.zone", "us-central1-a", true},
		{"gcp.zone", "europe west3 a", false},
		{"gcp.machine_type", "e2-standard-4", true},
		{"gcp.machine_type", "E2-medium", false}, // uppercase
		{"gcp.project", "valid-project-1", true},
		{"gcp.project", "no", false}, // too short
		{"gcp.project", "Bad_Project", false},
	} {
		err := Set(&cfg, c.key, c.val)
		if c.ok && err != nil {
			t.Errorf("Set(%q, %q) should succeed, got %v", c.key, c.val, err)
		}
		if !c.ok && err == nil {
			t.Errorf("Set(%q, %q) should be rejected, but was accepted", c.key, c.val)
		}
	}
}

// A config from before one default model keeps the default agent's model
// and effort, and the next write drops the per-agent keys.
func TestAgentsLegacyModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "[agents]\ndefault = \"codex\"\nclaude_model = \"opus\"\ncodex_model = \"gpt-6-astra\"\ncodex_effort = \"high\"\n"
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Agents.Model != "gpt-6-astra" || c.Agents.Effort != "high" {
		t.Errorf("carried over %q %q", c.Agents.Model, c.Agents.Effort)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "claude_model") || strings.Contains(string(b), "codex_model") {
		t.Errorf("old keys kept:\n%s", b)
	}
}

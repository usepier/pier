package config

import (
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
	// sensible default (credentials, org-specific ids) are empty until setup.
	emptyOK := map[string]bool{"aws.profile": true, "aws.region": true, "aws.subnet": true, "gcp.project": true}
	for _, s := range Settings {
		if Get(cfg, s.Key) == "" && !emptyOK[s.Key] {
			t.Errorf("Get(%q) is empty on a default config", s.Key)
		}
	}
}

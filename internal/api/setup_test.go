package api

import (
	"strings"
	"testing"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier/wire"
)

func TestSetupDraft(t *testing.T) {
	base := config.Default()
	d := draftFrom(base)
	d.Cloud, d.GCPProject, d.GCPZone = "gcp", "acme-dev", "europe-west4-a"
	d.Machine, d.DiskGiB, d.IdleTimeout, d.Speed = "e2-standard-4", 80, "1h", "lean"
	cfg, err := configFrom(base, d)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != "gcp-gce" || cfg.GCP.Project != "acme-dev" || cfg.GCP.MachineType != "e2-standard-4" ||
		cfg.GCP.DiskGiB != 80 || cfg.IdleTimeout != "1h" || cfg.Profile() != "lean" {
		t.Errorf("config = %+v", cfg)
	}
	if back := draftFrom(cfg); back.Machine != "e2-standard-4" || back.DiskGiB != 80 || back.Speed != "lean" {
		t.Errorf("draft = %+v", back)
	}
	d.IdleTimeout = "soon"
	if _, err := configFrom(base, d); err == nil {
		t.Error("an invalid idle timeout was accepted")
	}
	d.Cloud = "azure"
	if _, err := configFrom(base, d); err == nil {
		t.Error("an unknown cloud was accepted")
	}
}

func TestSetupAdminDoc(t *testing.T) {
	c := start(t, &fake{})
	resp, _ := c.call("setup.adminDoc", map[string]any{"cloud": "gcp-gce"})
	var doc wire.LogText
	remarshal(t, resp["result"], &doc)
	if !strings.Contains(doc.Text, "gcloud compute firewall-rules create pier-deny-ingress") {
		t.Errorf("doc = %q", doc.Text)
	}
}

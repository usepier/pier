package gcpgce

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
)

var requiredAPIs = []string{"compute.googleapis.com", "iap.googleapis.com"}

// SetupOnce creates the tiny per-project groundwork: two API enables and one
// firewall rule admitting only Google's IAP range to :22 on pier-tagged VMs.
// Idempotent — a second dev on the same project finds it all in Existed.
func (d *Driver) SetupOnce(ctx context.Context) (driver.SetupReport, error) {
	var rep driver.SetupReport
	note := func(created bool, what string) {
		if created {
			rep.Created = append(rep.Created, what)
		} else {
			rep.Existed = append(rep.Existed, what)
		}
	}

	enabled, err := d.gcloud(ctx, "services", "list", "--enabled",
		"--filter", "name:("+strings.Join(requiredAPIs, " OR ")+")",
		"--format", "value(config.name)")
	if err != nil {
		return rep, err
	}
	var missing []string
	for _, api := range requiredAPIs {
		if strings.Contains(enabled, api) {
			note(false, "API "+api)
		} else {
			missing = append(missing, api)
		}
	}
	if len(missing) > 0 {
		// First enable on a project takes a minute or two; gcloud waits.
		if _, err := d.gcloud(ctx, append([]string{"services", "enable"}, missing...)...); err != nil {
			return rep, err
		}
		for _, api := range missing {
			note(true, "API "+api)
		}
	}

	if _, err := d.gcloud(ctx, "compute", "firewall-rules", "describe", FirewallRule,
		"--format", "value(name)"); err != nil {
		if _, err := d.gcloud(ctx, "compute", "firewall-rules", "create", FirewallRule,
			"--network", "default",
			"--direction", "INGRESS", "--action", "ALLOW", "--rules", "tcp:22",
			"--source-ranges", IAPRange,
			"--priority", "999",
			"--target-tags", NetworkTag); err != nil {
			return rep, err
		}
		note(true, "firewall rule "+FirewallRule+" (IAP range -> :22)")
	} else {
		note(false, "firewall rule "+FirewallRule)
	}
	// The deny (1000) sits under the allow (999) but over any permissive rule
	// the shared network carries — default networks ship default-allow-ssh
	// open to the world, and pier VMs hold an external IP for egress.
	if _, err := d.gcloud(ctx, "compute", "firewall-rules", "describe", FirewallDeny,
		"--format", "value(name)"); err != nil {
		if _, err := d.gcloud(ctx, "compute", "firewall-rules", "create", FirewallDeny,
			"--network", "default",
			"--direction", "INGRESS", "--action", "DENY", "--rules", "all",
			"--source-ranges", "0.0.0.0/0",
			"--priority", "1000",
			"--target-tags", NetworkTag); err != nil {
			return rep, err
		}
		note(true, "firewall rule "+FirewallDeny+" (everything else stays out)")
	} else {
		note(false, "firewall rule "+FirewallDeny)
	}
	return rep, nil
}

// Teardown removes the groundwork. Refuses while sessions exist. APIs stay
// enabled: disabling compute would break everything else in the project.
func (d *Driver) Teardown(ctx context.Context) error {
	sessions, err := d.List(ctx)
	if err != nil {
		return err
	}
	if len(sessions) > 0 {
		return fmt.Errorf("%d session(s) still exist — `pier rm` them first", len(sessions))
	}

	for _, rule := range []string{FirewallRule, FirewallDeny} {
		if _, err := d.gcloud(ctx, "compute", "firewall-rules", "describe", rule,
			"--format", "value(name)"); err == nil {
			if _, err := d.gcloud(ctx, "compute", "firewall-rules", "delete", rule); err != nil {
				return err
			}
		}
	}

	// Baked images are repo-specific, so there can be several; every one
	// carries pier-managed — sweep by label.
	images, _ := d.gcloud(ctx, "compute", "images", "list",
		"--filter", "labels."+LabelManaged+"=1", "--format", "value(name)")
	for _, img := range strings.Fields(images) {
		_, _ = d.gcloud(ctx, "compute", "images", "delete", img)
	}
	return nil
}

func (d *Driver) Doctor(ctx context.Context) []driver.Check {
	var checks []driver.Check
	tool := func(name, bin, hint string) {
		_, err := exec.LookPath(bin)
		c := driver.Check{Name: name, OK: err == nil}
		if err != nil {
			c.Detail = hint
		}
		checks = append(checks, c)
	}
	tool("gcloud CLI", "gcloud", "install the Google Cloud CLI — cloud.google.com/sdk/docs/install")
	tool("ssh + ssh-keygen", "ssh-keygen", "install OpenSSH")

	// Optional but load-bearing for GitHub repos: without any of these,
	// private repos ship the slow full bundle and sessions can't push.
	gh := driver.Check{Name: "github credential", OK: true}
	switch {
	case payload.GitHubToken() != "":
		gh.Detail = "token found — private-repo fast fetch + push from sessions"
	case exec.Command("ssh-add", "-l").Run() == nil:
		gh.Detail = "ssh agent only — fast fetch relays it; pushes work while attached (`gh auth login` for detached pushes)"
	default:
		gh.OK = false
		gh.Detail = "none found (optional) — private repos use the slow bundle path and sessions can't push; fix: `gh auth login`"
	}
	checks = append(checks, gh)

	me, err := d.user(ctx)
	if err != nil {
		checks = append(checks, driver.Check{Name: "GCP credentials", Detail: err.Error()})
		return checks // everything below needs credentials
	}
	checks = append(checks, driver.Check{Name: "GCP credentials", OK: true, Detail: me + " / " + d.Project})

	// One probe covers project reachability, the compute API enable, and both
	// firewall rules — the groundwork `pier setup` lays down. The deny rule
	// matters: without it sessions on a default network face the internet.
	_, allowErr := d.gcloud(ctx, "compute", "firewall-rules", "describe", FirewallRule,
		"--format", "value(name)")
	_, denyErr := d.gcloud(ctx, "compute", "firewall-rules", "describe", FirewallDeny,
		"--format", "value(name)")
	c := driver.Check{Name: "groundwork (APIs, firewall rules)", OK: allowErr == nil && denyErr == nil}
	if !c.OK {
		c.Detail = "run `pier setup`"
	}
	checks = append(checks, c)

	if d.SupervisorBin != nil {
		_, err := d.SupervisorBin(archOf(d.MachineType))
		c := driver.Check{Name: "embedded supervisor", OK: err == nil}
		if err != nil {
			c.Detail = err.Error()
		}
		checks = append(checks, c)
	}

	if q, err := d.Headroom(ctx); err == nil {
		checks = append(checks, driver.Check{
			Name: "CPU quota", OK: q.Limit-q.Used >= 2, Detail: q.Detail,
		})
	} else {
		checks = append(checks, driver.Check{Name: "CPU quota", OK: true, Detail: "unknown (regions describe failed)"})
	}
	return checks
}

// Headroom: the region's CPUS quota vs. what is running now (all instances
// in the region, not just pier's — the quota is project-wide).
func (d *Driver) Headroom(ctx context.Context) (driver.Quota, error) {
	out, err := d.gcloud(ctx, "compute", "regions", "describe", regionOf(d.Zone),
		"--format", "json(quotas)")
	if err != nil {
		return driver.Quota{}, err
	}
	var region struct {
		Quotas []struct {
			Metric string  `json:"metric"`
			Usage  float64 `json:"usage"`
			Limit  float64 `json:"limit"`
		} `json:"quotas"`
	}
	if err := json.Unmarshal([]byte(out), &region); err != nil {
		return driver.Quota{}, fmt.Errorf("parsing region quotas: %w", err)
	}
	for _, quota := range region.Quotas {
		if quota.Metric == "CPUS" {
			q := driver.Quota{Used: int(quota.Usage), Limit: int(quota.Limit)}
			q.Detail = fmt.Sprintf("%d/%d CPUs in use in %s", q.Used, q.Limit, regionOf(d.Zone))
			return q, nil
		}
	}
	return driver.Quota{}, fmt.Errorf("no CPUS quota reported for region %s", regionOf(d.Zone))
}

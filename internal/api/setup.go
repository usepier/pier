package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// Setup, as steps for a form: what's here, whether a login works, what the
// defaults mean, and finishing — the same steps `pier setup` asks around.

func setupTools(ts []pier.Tool) []wire.SetupTool {
	out := []wire.SetupTool{}
	for _, t := range ts {
		out = append(out, wire.SetupTool{Name: t.Name, Hint: t.Hint, Found: t.Found})
	}
	return out
}

func draftFrom(cfg config.Config) wire.SetupDraft {
	d := wire.SetupDraft{
		Cloud: cfg.Driver, AWSProfile: cfg.AWS.Profile, AWSRegion: cfg.AWS.Region, AWSSubnet: cfg.AWS.Subnet,
		GCPProject: cfg.GCP.Project, GCPZone: cfg.GCP.Zone,
		Machine: cfg.AWS.InstanceType, DiskGiB: cfg.AWS.DiskGiB,
		IdleTimeout: cfg.IdleTimeout, UnattendedCap: cfg.UnattendedCap,
		Speed: cfg.Profile(), Manifest: append([]string{}, cfg.Secrets.Manifest...),
	}
	if d.Cloud == "" {
		d.Cloud = "aws-ec2"
	}
	if d.Cloud == "gcp-gce" {
		d.Machine, d.DiskGiB = cfg.GCP.MachineType, cfg.GCP.DiskGiB
	}
	return d
}

// configFrom applies a draft over a config, through the same validation
// settings use.
func configFrom(base config.Config, d wire.SetupDraft) (config.Config, error) {
	cfg := base
	driver, err := pier.CloudDriver(d.Cloud)
	if err != nil {
		return cfg, invalidParams(err.Error())
	}
	cfg.Driver = driver
	if driver == "aws-ec2" {
		cfg.AWS.Profile, cfg.AWS.Region, cfg.AWS.Subnet = d.AWSProfile, d.AWSRegion, d.AWSSubnet
		if cfg.AWS.Profile == "" {
			cfg.AWS.Profile = "default"
		}
	} else {
		cfg.GCP.Project, cfg.GCP.Zone = d.GCPProject, d.GCPZone
	}
	set := func(key, val string) error {
		if val == "" {
			return nil
		}
		if err := config.Set(&cfg, key, val); err != nil {
			return invalidParams(err.Error())
		}
		return nil
	}
	for _, kv := range [][2]string{
		{pier.MachineKey(cfg), d.Machine},
		{"idle_timeout", d.IdleTimeout},
		{"unattended_cap", d.UnattendedCap},
	} {
		if err := set(kv[0], kv[1]); err != nil {
			return cfg, err
		}
	}
	if d.DiskGiB > 0 {
		if err := set(pier.DiskKey(cfg), strconv.Itoa(d.DiskGiB)); err != nil {
			return cfg, err
		}
	}
	if d.Speed != "" && d.Speed != "custom" {
		if err := cfg.ApplyProfile(d.Speed); err != nil {
			return cfg, invalidParams(err.Error())
		}
	}
	if d.Manifest != nil {
		cfg.Secrets.Manifest = d.Manifest
	}
	if d.ClaudeToken != "" {
		cfg.Secrets.ClaudeOAuthToken = d.ClaudeToken
	}
	return cfg, nil
}

func init() {
	handle("setup.detect", "What setup finds on this machine: tools, cloud logins, agent config, and the saved answers (else the defaults).",
		func(ctx context.Context, s *Server, _ *Call, _ wire.Empty) (wire.SetupState, error) {
			d := pier.Detect()
			st := wire.SetupState{
				Configured: d.Configured, Draft: draftFrom(d.Config),
				AWSTools: setupTools(pier.Tools("aws-ec2")), GCPTools: setupTools(pier.Tools("gcp-gce")),
				AWSProfiles: append([]string{}, d.AWSProfiles...), GCPAccount: d.GCPAccount, GCPProject: d.GCPProject,
				Agents: append([]string{}, d.Agents...), ClaudeAuth: d.ClaudeAuth,
				NeedsClaudeToken: pier.NeedsClaudeToken(d.Config), HasClaudeToken: d.Config.Secrets.ClaudeOAuthToken != "",
			}
			if st.Draft.GCPProject == "" {
				st.Draft.GCPProject = d.GCPProject
			}
			return st, nil
		})
	handle("setup.check", "Checks a cloud login: who it is (and the AWS profile's region). An expired one fails with kind auth_expired and the login that fixes it.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SetupCheckParams) (wire.SetupIdentity, error) {
			switch p.Cloud {
			case "aws-ec2":
				arn, err := pier.CheckAWS(ctx, p.Profile)
				return wire.SetupIdentity{Identity: arn, Region: pier.AWSRegion(p.Profile)}, err
			case "gcp-gce":
				acct, err := pier.CheckGCP(p.Project)
				return wire.SetupIdentity{Identity: acct}, err
			}
			return wire.SetupIdentity{}, invalidParams("cloud is aws-ec2 or gcp-gce")
		})
	handle("setup.defaults", "What a draft gives new sessions, row by row with costs, and the machines to choose from.",
		func(ctx context.Context, s *Server, _ *Call, p wire.SetupDraft) (wire.SetupDefaults, error) {
			cfg, err := configFrom(pier.Detect().Config, p)
			if err != nil {
				return wire.SetupDefaults{}, err
			}
			drv, err := pier.NewDriver(cfg, s.opts.Pier)
			if err != nil {
				return wire.SetupDefaults{}, err
			}
			home, _ := os.UserHomeDir()
			out := wire.SetupDefaults{Rows: []wire.DefaultRow{}, Machines: wireMachines(drv.Machines(p.Machine))}
			for _, r := range pier.DefaultRows(cfg, drv, pier.AgentDirs(home), true) {
				out.Rows = append(out.Rows, wire.DefaultRow{Key: r.Key, Label: r.Label, Value: r.Value, Note: r.Note})
			}
			return out, nil
		})
	handle("setup.apply", "Finishes setup: the account's groundwork, the config, the checks, the onboarding skill, the account's existing images.",
		func(ctx context.Context, s *Server, c *Call, p wire.SetupApplyParams) (wire.SetupResult, error) {
			cfg, err := configFrom(pier.Detect().Config, p.Draft)
			if err != nil {
				return wire.SetupResult{}, err
			}
			c.Progress(wire.Event{Kind: wire.EventStep, Message: "laying down the account's groundwork"})
			res, err := pier.ApplySetup(ctx, cfg, s.opts.Pier, p.Skills)
			out := wire.SetupResult{Created: nonNil(res.Created), Existed: nonNil(res.Existed), Checks: []wire.Check{},
				Skills: nonNil(res.Skills), Adopted: map[string]string{}}
			for _, ch := range res.Checks {
				st := wire.CheckOK
				if !ch.OK {
					st = wire.CheckFail
				}
				out.Checks = append(out.Checks, wire.Check{Name: ch.Name, Status: st, Detail: ch.Detail, Fix: ch.Fix})
			}
			for k, v := range res.Adopted {
				out.Adopted[k] = v
			}
			if err == nil {
				// The config changed under the backend: build it again.
				s.reopen()
			}
			return out, err
		})
	handle("setup.login", "Runs the login setup.check asked for — aws sso login for the profile, gcloud auth login — narrating it (browser prompts, device codes) as progress.",
		func(ctx context.Context, s *Server, c *Call, p wire.SetupCheckParams) (*wire.Empty, error) {
			var argv []string
			switch p.Cloud {
			case "aws-ec2":
				if !awsProfile.MatchString(p.Profile) {
					return nil, invalidParams("bad AWS profile")
				}
				argv = []string{"aws", "sso", "login", "--profile", p.Profile}
			case "gcp-gce":
				argv = []string{"gcloud", "auth", "login"}
			default:
				return nil, invalidParams("cloud is aws-ec2 or gcp-gce")
			}
			return nil, runLogin(ctx, c, exec.Command(argv[0], argv[1:]...))
		})
	handle("setup.adminDoc", "The commands an admin runs when the person setting pier up can't create its groundwork.",
		func(ctx context.Context, s *Server, _ *Call, p wire.CloudRef) (wire.LogText, error) {
			if _, err := pier.CloudDriver(p.Cloud); err != nil {
				return wire.LogText{}, invalidParams(fmt.Sprint(err))
			}
			return wire.LogText{Text: pier.AdminDoc(p.Cloud)}, nil
		})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

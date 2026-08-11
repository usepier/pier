package awsec2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
)

// Create: launch first, prep the workspace bundle while the instance boots,
// push over SSH-over-SSM as soon as sshd answers, bootstrap, done. On a stock
// AMI the bootstrap waits for cloud-init's harness install (minutes, once);
// on a baked AMI the whole thing is the boot + push time. A create that
// fails after launch destroys its own instance — a session either exists
// fully set up or not at all, never as a half-made husk in the list.
func (d *Driver) Create(ctx context.Context, spec driver.CreateSpec) (sess *driver.Session, retErr error) {
	progress := spec.Progress
	if progress == nil {
		progress = func(string) {}
	}
	if err := payload.ValidateNames(spec); err != nil {
		return nil, err
	}
	me, err := d.user(ctx)
	if err != nil {
		return nil, err
	}

	arch, err := d.archOf(ctx, d.InstanceType)
	if err != nil {
		return nil, err
	}
	supervisor, err := d.SupervisorBin(arch)
	if err != nil {
		return nil, err
	}
	ami, err := d.resolveAMI(ctx, arch, spec.Image)
	if err != nil {
		return nil, err
	}

	// The keypair is instance-id-keyed but must exist pre-launch (pubkey goes
	// into user-data), so generate under a temp name and rename after launch.
	tmpID := "new-" + payload.Sanitize(spec.Name)
	pubkey, err := d.newKeypair(tmpID)
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "pier-create-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	userData := payload.RenderUserData(spec, pubkey)
	udPath := filepath.Join(work, "user-data.yaml")
	if err := os.WriteFile(udPath, []byte(userData), 0o600); err != nil {
		return nil, err
	}

	id, err := d.launch(ctx, spec, me, ami, udPath)
	if err != nil {
		return nil, err
	}
	os.Rename(d.keyPath(tmpID), d.keyPath(id))
	os.Rename(d.keyPath(tmpID)+".pub", d.keyPath(id)+".pub")
	progress(fmt.Sprintf("launched %s (%s, %s)", id, d.InstanceType, arch))
	// WithoutCancel: the cleanup must run even when the failure IS the ctx
	// being cancelled (ctrl-c mid-create).
	defer func() {
		if retErr == nil {
			return
		}
		progress("create failed — terminating the half-made instance")
		if err := d.Destroy(context.WithoutCancel(ctx), id); err != nil {
			progress("cleanup failed (" + err.Error() + ") — remove it with `pier rm " + spec.Name + "`")
		}
	}()

	// Local prep while the instance boots (~30s).
	pl, err := payload.Build(ctx, work, spec, supervisor, d.Manifest, d.SessionEnv, progress)
	if err != nil {
		return nil, err
	}

	progress("waiting for SSH over SSM (boot ~30s)")
	if err := d.waitSSH(ctx, id, 240*time.Second); err != nil {
		return nil, err
	}

	for _, n := range pl.Notes {
		progress(n)
	}
	for _, p := range pl.Pushes {
		if err := d.scpTo(ctx, id, p.Local, p.Remote); err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			// One retry: the SSM tunnel can drop right as the instance
			// settles ("lost connection" seconds after waitSSH passed).
			progress("push interrupted — retrying")
			time.Sleep(2 * time.Second)
			if err := d.scpTo(ctx, id, p.Local, p.Remote); err != nil {
				return nil, err
			}
		}
	}

	progress("bootstrapping (stock AMI waits for cloud-init here — `pier bake` skips that)")
	var fwd []string
	if pl.ForwardAgent {
		fwd = []string{"-A"}
	}
	if out, err := d.sshRunOpts(ctx, id, fwd, "bash /tmp/pier-bootstrap.sh"); err != nil {
		return nil, fmt.Errorf("bootstrap: %w\n%s", err, out)
	}
	// Last act: the ready tag is what List and attach trust — running
	// without it reads as still-creating everywhere. A failed tag write
	// fails the create (defer cleans up) rather than leave a session that
	// looks stuck forever.
	if _, err := d.aws(ctx, "ec2", "create-tags", "--resources", id,
		"--tags", "Key="+TagReady+",Value=1"); err != nil {
		return nil, fmt.Errorf("marking session ready: %w", err)
	}

	return &driver.Session{
		ID: id, Name: spec.Name, Repo: filepath.Base(spec.Repo), Branch: spec.Branch,
		User: me, Driver: d.Name(), State: driver.StateRunning, Created: time.Now(),
		InstanceType: d.InstanceType,
		CostNote:     costNote(driver.StateRunning, d.InstanceType),
	}, nil
}

// launch retries on IAM instance-profile propagation lag (the spike showed
// one retry is usually needed right after `pier setup`).
func (d *Driver) launch(ctx context.Context, spec driver.CreateSpec, me, ami, udPath string) (string, error) {
	sg, err := d.securityGroupID(ctx)
	if err != nil {
		return "", err
	}
	if sg == "" {
		return "", fmt.Errorf("security group %s not found — run `pier setup`", SecurityGroup)
	}
	rootDev, err := d.aws(ctx, "ec2", "describe-images", "--image-ids", ami,
		"--query", "Images[0].RootDeviceName", "--output", "text")
	if err != nil {
		return "", err
	}

	tags, _ := json.Marshal([]map[string]any{
		{"ResourceType": "instance", "Tags": tagList(spec, me, "pier-"+spec.Name)},
		{"ResourceType": "volume", "Tags": tagList(spec, me, "pier-"+spec.Name)},
	})
	bdm := fmt.Sprintf(`[{"DeviceName":%q,"Ebs":{"VolumeSize":%d,"VolumeType":"gp3","DeleteOnTermination":true}}]`,
		rootDev, d.DiskGiB)

	args := []string{"ec2", "run-instances",
		"--image-id", ami,
		"--instance-type", d.InstanceType,
		"--iam-instance-profile", "Name=" + ProfileName,
		"--security-group-ids", sg,
		"--instance-initiated-shutdown-behavior", "stop", // in-VM shutdown = park
		"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
		"--block-device-mappings", bdm,
		"--tag-specifications", string(tags),
		"--user-data", "file://" + udPath,
		"--query", "Instances[0].InstanceId",
	}
	if d.Subnet != "" {
		args = append(args, "--subnet-id", d.Subnet)
	}

	var lastErr error
	for range 10 {
		out, err := d.aws(ctx, append(args, "--output", "text")...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if strings.Contains(err.Error(), "Invalid IAM Instance Profile") {
			time.Sleep(3 * time.Second) // IAM propagation
			continue
		}
		if strings.Contains(err.Error(), "VcpuLimitExceeded") {
			return "", fmt.Errorf("vCPU quota exceeded — request a raise:\n"+
				"  aws service-quotas request-service-quota-increase --service-code ec2 --quota-code L-1216C47A --desired-value 32\n(%w)", err)
		}
		return "", err
	}
	return "", lastErr
}

func tagList(spec driver.CreateSpec, me, name string) []map[string]string {
	return []map[string]string{
		{"Key": "Name", "Value": name},
		{"Key": TagManaged, "Value": "1"},
		{"Key": TagUser, "Value": me},
		{"Key": TagSession, "Value": spec.Name},
		{"Key": TagRepo, "Value": filepath.Base(spec.Repo)},
		{"Key": TagBranch, "Value": spec.Branch},
		{"Key": TagCreated, "Value": time.Now().UTC().Format(time.RFC3339)},
	}
}

// archOf maps the instance type to arm64/amd64 (selects AMI + supervisor build).
func (d *Driver) archOf(ctx context.Context, itype string) (string, error) {
	out, err := d.aws(ctx, "ec2", "describe-instance-types", "--instance-types", itype,
		"--query", "InstanceTypes[0].ProcessorInfo.SupportedArchitectures[0]", "--output", "text")
	if err != nil {
		return "", err
	}
	if out == "x86_64" {
		return "amd64", nil
	}
	return out, nil
}

// resolveAMI picks the launch image: the caller's baked AMI (repo-specific,
// from config) when given, otherwise the stock Ubuntu the SSM parameter names.
func (d *Driver) resolveAMI(ctx context.Context, arch, image string) (string, error) {
	if image != "" {
		return image, nil
	}
	return d.aws(ctx, "ssm", "get-parameter",
		"--name", fmt.Sprintf(amiParamBase, arch),
		"--query", "Parameter.Value", "--output", "text")
}

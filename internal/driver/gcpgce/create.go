package gcpgce

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
)

// Create: launch first, prep the workspace bundle while the instance boots,
// push over the IAP tunnel as soon as sshd answers, bootstrap, done. On a
// stock image the bootstrap waits for cloud-init's harness install (minutes,
// once); on a baked image the whole thing is the boot + push time. A create
// that fails after launch destroys its own instance — a session either
// exists fully set up or not at all, never as a half-made husk in the list.
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

	arch := archOf(d.MachineType)
	supervisor, err := d.SupervisorBin(arch)
	if err != nil {
		return nil, err
	}

	// The instance name is the session ID and is known before launch, so the
	// keypair needs no temp-name dance: it is id-keyed from the start. The
	// pubkey rides instance metadata — the guest agent maintains the agent
	// user's authorized_keys from it, and the shared user-data seeds it too.
	id := instanceName(spec.Name, me)
	pubkey, err := d.newKeypair(id)
	if err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "pier-create-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	udPath := filepath.Join(work, "user-data.yaml")
	if err := os.WriteFile(udPath, []byte(payload.RenderUserData(spec, pubkey)), 0o600); err != nil {
		return nil, err
	}

	if err := d.launch(ctx, spec, me, id, pubkey, udPath); err != nil {
		return nil, err
	}
	progress(fmt.Sprintf("launched %s (%s, %s)", id, d.MachineType, arch))
	// WithoutCancel: the cleanup must run even when the failure IS the ctx
	// being cancelled (ctrl-c mid-create).
	defer func() {
		if retErr == nil {
			return
		}
		progress("create failed — deleting the half-made instance")
		if err := d.Destroy(context.WithoutCancel(ctx), id); err != nil {
			progress("cleanup failed (" + err.Error() + ") — remove it with `pier rm " + spec.Name + "`")
		}
	}()

	// Local prep while the instance boots (~60s).
	pl, err := payload.Build(ctx, work, spec, supervisor, d.Manifest, d.SessionEnv, progress)
	if err != nil {
		return nil, err
	}

	progress("waiting for SSH over the IAP tunnel (boot ~60s)")
	if err := d.waitSSH(ctx, id, 300*time.Second); err != nil {
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
			// One retry: the tunnel can drop right as the instance settles
			// ("lost connection" seconds after waitSSH passed).
			progress("push interrupted — retrying")
			time.Sleep(2 * time.Second)
			if err := d.scpTo(ctx, id, p.Local, p.Remote); err != nil {
				return nil, err
			}
		}
	}

	progress("bootstrapping (a stock image waits for cloud-init here — `pier bake` skips that)")
	var fwd []string
	if pl.ForwardAgent {
		fwd = []string{"-A"}
	}
	if out, err := d.sshRunOpts(ctx, id, fwd, "bash /tmp/pier-bootstrap.sh"); err != nil {
		return nil, fmt.Errorf("bootstrap: %w\n%s", err, out)
	}
	// Last act: the ready label is what List and attach trust — running
	// without it reads as still-creating everywhere. A failed label write
	// fails the create (defer cleans up) rather than leave a session that
	// looks stuck forever.
	if _, err := d.gcloud(ctx, "compute", "instances", "add-labels", id,
		"--zone", d.Zone, "--labels", LabelReady+"=1"); err != nil {
		return nil, fmt.Errorf("marking session ready: %w", err)
	}

	return &driver.Session{
		ID: id, Name: spec.Name, Repo: filepath.Base(spec.Repo), Branch: spec.Branch,
		User: me, Driver: d.Name(), State: driver.StateRunning, Created: time.Now(),
		InstanceType: d.MachineType,
		CostNote:     costNote(driver.StateRunning, d.MachineType),
	}, nil
}

// launch creates the instance: no service account and no scopes (zero cloud
// permissions inside the VM), the IAP firewall's network tag, an ephemeral
// external IP for egress only, and a boot disk that dies with the instance.
// Filterable identity goes in labels; freeform values in metadata (GCE label
// values only allow [a-z0-9_-]). Metadata values are comma-joined on the
// flag, which ValidateNames' charset and the key format keep safe.
func (d *Driver) launch(ctx context.Context, spec driver.CreateSpec, me, id, pubkey, udPath string) error {
	meta := strings.Join([]string{
		"ssh-keys=agent:" + pubkey,
		MetaSession + "=" + spec.Name,
		MetaRepo + "=" + filepath.Base(spec.Repo),
		MetaBranch + "=" + spec.Branch,
		MetaUser + "=" + me,
	}, ",")
	args := []string{"compute", "instances", "create", id,
		"--zone", d.Zone,
		"--machine-type", d.MachineType,
		"--boot-disk-size", fmt.Sprintf("%dGB", d.DiskGiB),
		"--boot-disk-type", "pd-balanced",
		"--no-service-account", "--no-scopes",
		"--tags", NetworkTag,
		"--labels", LabelManaged + "=1," + LabelUser + "=" + labelValue(me),
		"--metadata-from-file", "user-data=" + udPath,
		"--metadata", meta,
		"--format", "value(name)",
	}
	if spec.Image != "" {
		args = append(args, "--image", spec.Image)
	} else {
		args = append(args, "--image-family", fmt.Sprintf(stockImageFamily, archOf(d.MachineType)),
			"--image-project", stockImageProject)
	}
	_, err := d.gcloud(ctx, args...)
	if err != nil && strings.Contains(strings.ToUpper(err.Error()), "QUOTA") {
		return fmt.Errorf("CPU quota exceeded in %s — request a raise in the console (IAM & Admin → Quotas, metric CPUs, region %s)\n(%w)",
			regionOf(d.Zone), regionOf(d.Zone), err)
	}
	return err
}

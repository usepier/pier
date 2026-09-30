package awsec2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/driver/payload"
	"github.com/usepier/pier/internal/ui"
)

// Bake launches a throwaway instance with the exact session user-data, lets
// cloud-init finish the harness install, runs the repo's .pier/bake.sh (if
// any), prebuilds the repo (checkout + .pier/setup.sh to completion, then a
// scrub), and images the result. Every install step in the user-data is
// guarded and the bootstrap reuses a checkout it finds, so sessions launched
// from the baked AMI skip both the harness install and the repo's cold setup.
// Images are repo-specific: the hook is where a repo's toolchains (pnpm,
// python, ...) get baked in, setup.sh where its dependencies do.
func (d *Driver) Bake(ctx context.Context, spec driver.BakeSpec) (driver.Baked, error) {
	arch, err := d.archOf(ctx, d.InstanceType)
	if err != nil {
		return driver.Baked{}, err
	}
	// Always bake from the stock Ubuntu AMI (not a previous bake) so images
	// don't accrete layers.
	ami, err := d.resolveAMI(ctx, arch, "")
	if err != nil {
		return driver.Baked{}, err
	}

	me, err := d.user(ctx)
	if err != nil {
		return driver.Baked{}, err
	}
	cspec := driver.CreateSpec{Name: "bake", Repo: "bake", Branch: "bake"} // timeouts 0 = never park mid-bake
	pub, err := d.newKeypair("bake")
	if err != nil {
		return driver.Baked{}, err
	}
	defer os.Remove(d.keyPath("bake"))
	defer os.Remove(d.keyPath("bake") + ".pub")

	work, err := os.MkdirTemp("", "pier-bake-*")
	if err != nil {
		return driver.Baked{}, err
	}
	defer os.RemoveAll(work)
	udPath := filepath.Join(work, "user-data.yaml")
	if err := os.WriteFile(udPath, []byte(payload.RenderUserData(cspec, pub)), 0o600); err != nil {
		return driver.Baked{}, err
	}

	id, err := d.launch(ctx, cspec, me, ami, udPath)
	if err != nil {
		return driver.Baked{}, err
	}
	// A failed bake leaves nothing behind. A successful one hands the stopped
	// instance to FinishBake, which terminates it once the image is saved.
	handedOff := false
	defer func() {
		if !handedOff {
			d.aws(context.WithoutCancel(ctx), "ec2", "terminate-instances", "--instance-ids", id)
		}
	}()
	os.Rename(d.keyPath("bake"), d.keyPath(id))
	os.Rename(d.keyPath("bake")+".pub", d.keyPath(id)+".pub")
	defer os.Remove(d.keyPath(id))
	defer os.Remove(d.keyPath(id) + ".pub")
	spec.Step("bake instance " + id + " launched — installing harnesses (a few minutes)")

	if err := d.waitSSH(ctx, id, 240*time.Second); err != nil {
		return driver.Baked{}, err
	}
	// On failure, say WHICH harness is missing and show the install log tail
	// — "did not complete" alone sends people digging through a VM that this
	// function is about to terminate.
	verify := `sudo cloud-init status --wait >/dev/null
miss=""
for c in claude codex gh; do command -v "$c" >/dev/null || miss="$miss $c"; done
[ -z "$miss" ] && exit 0
echo "not installed:$miss — cloud-init log tail:"
sudo tail -n 25 /var/log/cloud-init-output.log
exit 1`
	if _, err := d.sshRun(ctx, id, verify); err != nil {
		return driver.Baked{}, fmt.Errorf("harness install did not complete: %w", err)
	}
	if spec.HookPath != "" {
		spec.Step("running .pier/bake.sh (output follows)")
		if err := d.scpTo(ctx, id, spec.HookPath, "/tmp/pier-bake.sh", "-q"); err != nil {
			return driver.Baked{}, err
		}
		if err := d.sshStream(ctx, id, "bash /tmp/pier-bake.sh && rm -f /tmp/pier-bake.sh"); err != nil {
			return driver.Baked{}, fmt.Errorf(".pier/bake.sh failed — nothing baked: %w", err)
		}
	}
	if spec.RepoRoot != "" {
		supervisor, err := d.SupervisorBin(arch)
		if err != nil {
			return driver.Baked{}, err
		}
		step := func(s string) { fmt.Println(ui.Step(s)) }
		remote := payload.Remote{
			Push: func(ctx context.Context, local, remote string) error { return d.push(ctx, id, local, remote, step) },
			Run: func(ctx context.Context, extra []string, script string) (string, error) {
				return d.sshRunOpts(ctx, id, extra, script)
			},
		}
		if err := payload.Prebuild(ctx, spec.RepoRoot, supervisor, d.Manifest, d.SessionEnv, remote, step); err != nil {
			return driver.Baked{}, fmt.Errorf("prebuild failed — nothing baked: %w", err)
		}
	}
	// Per-instance state must not leak into the image.
	if _, err := d.sshRun(ctx, id, "rm -f ~/.ssh/authorized_keys && sudo rm -rf /etc/pier"); err != nil {
		return driver.Baked{}, err
	}

	spec.Step("imaging — stop, snapshot, register (a few minutes)")
	if _, err := d.aws(ctx, "ec2", "stop-instances", "--instance-ids", id); err != nil {
		return driver.Baked{}, err
	}
	if _, err := d.aws(ctx, "ec2", "wait", "instance-stopped", "--instance-ids", id); err != nil {
		return driver.Baked{}, err
	}
	repo := payload.Sanitize(spec.RepoName)
	name := "pier-" + repo + "-" + time.Now().Format("20060102-1504")
	// The owner tag is what lets Images find this image again without the
	// config, and keeps it apart from teammates' images in a shared account.
	tags := []map[string]string{
		{"Key": TagManaged, "Value": "1"},
		{"Key": TagRepo, "Value": repo},
		{"Key": TagUser, "Value": me},
	}
	tagSpecs, err := json.Marshal([]map[string]any{
		{"ResourceType": "image", "Tags": tags},
		{"ResourceType": "snapshot", "Tags": tags},
	})
	if err != nil {
		return driver.Baked{}, err
	}
	img, err := d.aws(ctx, "ec2", "create-image", "--instance-id", id, "--name", name,
		"--description", "pier session base for "+repo+" (harnesses, bake hook, prebuilt checkout)",
		"--tag-specifications", string(tagSpecs),
		"--query", "ImageId", "--output", "text")
	if err != nil {
		return driver.Baked{}, err
	}
	handedOff = true
	return driver.Baked{Image: img, Instance: id}, nil
}

// FinishBake waits for the image to become available, then terminates the
// bake instance.
func (d *Driver) FinishBake(ctx context.Context, b driver.Baked, step func(string)) error {
	err := d.waitImage(ctx, b.Image, step)
	if ctx.Err() != nil || errors.Is(err, errImagePending) {
		// The image may yet finish: leave it (image reconcile adopts it) and
		// its instance (the sweep collects stale bake instances).
		return err
	}
	if _, terr := d.aws(context.WithoutCancel(ctx), "ec2", "terminate-instances", "--instance-ids", b.Instance); terr != nil && err == nil {
		return terr
	}
	if err != nil {
		_ = d.DeleteImage(context.WithoutCancel(ctx), b.Image) // it failed: nothing will ever use it
	}
	return err
}

// errImagePending: the image outlived the wait but hasn't failed.
var errImagePending = errors.New("image still saving")

// Images lists the caller's available pier images, newest first.
func (d *Driver) Images(ctx context.Context) ([]driver.Image, error) {
	me, err := d.user(ctx)
	if err != nil {
		return nil, err
	}
	out, err := d.aws(ctx, "ec2", "describe-images", "--owners", "self",
		"--filters", "Name=tag:"+TagManaged+",Values=1", "Name=state,Values=available",
		"--query", "Images[].[ImageId,CreationDate,Tags[?Key=='"+TagRepo+"'].Value|[0],Tags[?Key=='"+TagUser+"'].Value|[0]]",
		"--output", "json")
	if err != nil {
		return nil, err
	}
	var rows [][]*string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("describe-images: %w", err)
	}
	var imgs []driver.Image
	for _, r := range rows {
		if len(r) < 4 || r[0] == nil || r[2] == nil {
			continue
		}
		if r[3] != nil && *r[3] != me {
			continue // a teammate's
		}
		img := driver.Image{ID: *r[0], Repo: *r[2]}
		if r[1] != nil {
			img.Created, _ = time.Parse(time.RFC3339, *r[1])
		}
		imgs = append(imgs, img)
	}
	slices.SortFunc(imgs, func(a, b driver.Image) int { return b.Created.Compare(a.Created) })
	return imgs, nil
}

// DeleteImage deregisters the image and deletes its snapshots.
func (d *Driver) DeleteImage(ctx context.Context, id string) error {
	snaps, err := d.aws(ctx, "ec2", "describe-images", "--image-ids", id,
		"--query", "Images[0].BlockDeviceMappings[].Ebs.SnapshotId", "--output", "text")
	if err != nil {
		return err
	}
	if _, err := d.aws(ctx, "ec2", "deregister-image", "--image-id", id); err != nil {
		return err
	}
	for _, s := range strings.Fields(snaps) {
		if s == "None" {
			continue
		}
		if _, err := d.aws(ctx, "ec2", "delete-snapshot", "--snapshot-id", s); err != nil {
			return err
		}
	}
	return nil
}

// imageWait bounds how long a bake waits for its image. A prebuilt image
// carries the repo's dependencies and container images, and snapshotting that
// routinely outlasts the CLI waiter's fixed ten minutes — which used to fail a
// bake whose image was still on its way to being fine.
var imageWait, imagePoll = 90 * time.Minute, 20 * time.Second

// waitImage polls the image until it's available, reporting the snapshot's
// progress about once a minute so a long wait doesn't read as a hang.
func (d *Driver) waitImage(ctx context.Context, img string, step func(string)) error {
	deadline := time.Now().Add(imageWait)
	lastNote := time.Now()
	for {
		out, err := d.aws(ctx, "ec2", "describe-images", "--image-ids", img,
			"--query", "Images[0].[State,BlockDeviceMappings[0].Ebs.SnapshotId]", "--output", "text")
		if err != nil {
			return err
		}
		f := strings.Fields(out)
		state := ""
		if len(f) > 0 {
			state = f[0]
		}
		switch state {
		case "available":
			return nil
		case "failed", "invalid", "deregistered", "error":
			return fmt.Errorf("image %s ended %s", img, state)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s still %s after %s — it may yet finish; check it in the EC2 console", errImagePending, img, state, imageWait)
		}
		if len(f) > 1 && f[1] != "None" && time.Since(lastNote) >= time.Minute {
			if pct, err := d.aws(ctx, "ec2", "describe-snapshots", "--snapshot-ids", f[1],
				"--query", "Snapshots[0].Progress", "--output", "text"); err == nil {
				step("snapshot " + pct)
			}
			lastNote = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(imagePoll):
		}
	}
}

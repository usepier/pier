package gcpgce

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
// any), and images the boot disk. Because every install step in the user-data
// is guarded, sessions launched from the baked image skip straight past it.
// With a repo root the bake is a prebuild: the checkout and a completed
// .pier/setup.sh are imaged too, so creates skip the repo's cold setup.
// Images are repo-specific: the hook is where a repo's toolchains (pnpm,
// python, ...) get baked in, setup.sh where its dependencies do.
func (d *Driver) Bake(ctx context.Context, spec driver.BakeSpec) (driver.Baked, error) {
	me, err := d.user(ctx)
	if err != nil {
		return driver.Baked{}, err
	}
	cspec := driver.CreateSpec{Name: "bake", Repo: "bake", Branch: "bake"} // timeouts 0 = never park mid-bake
	// Unique per bake: a finished bake holds its instance until the image
	// is saved, and the next bake must not collide with it.
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		return driver.Baked{}, err
	}
	id := instanceName("bake-"+hex.EncodeToString(suffix), me)
	pub, err := d.newKeypair(id)
	if err != nil {
		return driver.Baked{}, err
	}
	defer os.Remove(d.keyPath(id))
	defer os.Remove(d.keyPath(id) + ".pub")

	work, err := os.MkdirTemp("", "pier-bake-*")
	if err != nil {
		return driver.Baked{}, err
	}
	defer os.RemoveAll(work)
	udPath := filepath.Join(work, "user-data.yaml")
	if err := os.WriteFile(udPath, []byte(payload.RenderUserData(cspec, pub)), 0o600); err != nil {
		return driver.Baked{}, err
	}

	// cspec.Image is empty, so launch always bakes from the stock Ubuntu image
	// (not a previous bake) — images don't accrete layers.
	if err := d.launch(ctx, cspec, me, id, pub, udPath); err != nil {
		return driver.Baked{}, err
	}
	// A failed bake leaves nothing behind. A successful one hands the stopped
	// instance to FinishBake: GCE won't delete a disk an image is still being
	// created from.
	handedOff := false
	defer func() {
		if !handedOff {
			d.Destroy(context.WithoutCancel(ctx), id)
		}
	}()
	spec.Step("bake instance " + id + " launched — installing harnesses (a few minutes)")

	if err := d.waitSSH(ctx, id, 300*time.Second); err != nil {
		return driver.Baked{}, err
	}
	// On failure, say WHICH harness is missing and show the install log tail
	// — "did not complete" alone sends people digging through a VM that this
	// function is about to delete.
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
		supervisor, err := d.SupervisorBin(archOf(d.MachineType))
		if err != nil {
			return driver.Baked{}, err
		}
		remote := payload.Remote{
			Push: func(ctx context.Context, local, remote string) error {
				if err := d.scpTo(ctx, id, local, remote); err == nil || ctx.Err() != nil {
					return err
				}
				// One retry, as in create: the tunnel can drop mid-push.
				time.Sleep(2 * time.Second)
				return d.scpTo(ctx, id, local, remote)
			},
			Run: func(ctx context.Context, extra []string, script string) (string, error) {
				return d.sshRunOpts(ctx, id, extra, script)
			},
		}
		step := func(s string) { fmt.Println(ui.Step(s)) }
		if err := payload.Prebuild(ctx, spec.RepoRoot, supervisor, d.Manifest, d.SessionEnv, remote, step); err != nil {
			return driver.Baked{}, fmt.Errorf("prebuild failed — nothing baked: %w", err)
		}
	}
	// Per-instance state must not leak into the image.
	if _, err := d.sshRun(ctx, id, "rm -f ~/.ssh/authorized_keys && sudo rm -rf /etc/pier"); err != nil {
		return driver.Baked{}, err
	}

	spec.Step("imaging — stop, snapshot the boot disk (a few minutes)")
	// Synchronous stop: images create wants the disk quiesced.
	if _, err := d.gcloud(ctx, "compute", "instances", "stop", id, "--zone", d.Zone); err != nil {
		return driver.Baked{}, err
	}
	repo := payload.Sanitize(spec.RepoName)
	// Image names cap at 63 chars: "pier-" + repo + "-20060102-1504".
	if len(repo) > 44 {
		repo = strings.TrimRight(repo[:44], "-")
	}
	name := "pier-" + repo + "-" + time.Now().Format("20060102-1504")
	// The boot disk shares the instance's name. --async: saving the image
	// can outlast the bake, and FinishBake waits it out.
	if _, err := d.gcloud(ctx, "compute", "images", "create", name,
		"--source-disk", id, "--source-disk-zone", d.Zone,
		"--description", "pier session base for "+repo+" (harnesses, bake hook, prebuilt checkout)",
		// The owner label is what lets Images find this image again without
		// the config, and keeps it apart from teammates' images.
		"--labels", LabelManaged+"=1,"+MetaRepo+"="+repo+","+LabelUser+"="+labelValue(me),
		"--async"); err != nil {
		return driver.Baked{}, err
	}
	handedOff = true
	return driver.Baked{Image: name, Instance: id}, nil
}

// imageWait bounds how long FinishBake waits for an image to become ready.
var imageWait, imagePoll = 90 * time.Minute, 20 * time.Second

// FinishBake waits for the image to become ready, then deletes the bake
// instance (and its disk).
func (d *Driver) FinishBake(ctx context.Context, b driver.Baked, step func(string)) error {
	deadline := time.Now().Add(imageWait)
	lastNote := time.Now()
	for {
		status, err := d.gcloud(ctx, "compute", "images", "describe", b.Image, "--format", "value(status)")
		if err != nil && ctx.Err() != nil {
			return err
		}
		switch {
		case err != nil:
			// A describe that fails right after the async create is the image
			// not being listed yet; keep polling until the deadline.
		case status == "READY":
			return d.Destroy(context.WithoutCancel(ctx), b.Instance)
		case status == "FAILED":
			_ = d.Destroy(context.WithoutCancel(ctx), b.Instance)
			_ = d.DeleteImage(context.WithoutCancel(ctx), b.Image)
			return fmt.Errorf("image %s failed", b.Image)
		}
		if time.Now().After(deadline) {
			// It may yet finish: leave the image (image reconcile adopts it)
			// and the instance (the sweep collects stale bake instances).
			return fmt.Errorf("image %s still %s after %s — it may yet finish", b.Image, cmp.Or(status, "unlisted"), imageWait)
		}
		if time.Since(lastNote) >= time.Minute {
			step("saving the image (" + strings.ToLower(cmp.Or(status, "pending")) + ")")
			lastNote = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(imagePoll):
		}
	}
}

// Images lists the caller's ready pier images, newest first.
func (d *Driver) Images(ctx context.Context) ([]driver.Image, error) {
	me, err := d.user(ctx)
	if err != nil {
		return nil, err
	}
	out, err := d.gcloud(ctx, "compute", "images", "list", "--no-standard-images",
		"--filter", "labels."+LabelManaged+"=1 AND status=READY",
		"--format", "json(name,creationTimestamp,labels)")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name    string            `json:"name"`
		Created string            `json:"creationTimestamp"`
		Labels  map[string]string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("images list: %w", err)
	}
	var imgs []driver.Image
	for _, r := range rows {
		if r.Labels[MetaRepo] == "" {
			continue
		}
		if u, ok := r.Labels[LabelUser]; ok && u != labelValue(me) {
			continue // a teammate's
		}
		img := driver.Image{ID: r.Name, Repo: r.Labels[MetaRepo]}
		img.Created, _ = time.Parse(time.RFC3339, r.Created)
		imgs = append(imgs, img)
	}
	slices.SortFunc(imgs, func(a, b driver.Image) int { return b.Created.Compare(a.Created) })
	return imgs, nil
}

// DeleteImage deletes the image (its storage goes with it).
func (d *Driver) DeleteImage(ctx context.Context, id string) error {
	_, err := d.gcloud(ctx, "compute", "images", "delete", id)
	return err
}

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
	"github.com/usepier/pier/internal/ui"
)

// Bake launches a throwaway instance with the exact session user-data, lets
// cloud-init finish the harness install, runs the repo's .pier-bake.sh (if
// any), and images the boot disk. Because every install step in the user-data
// is guarded, sessions launched from the baked image skip straight past it —
// cold create drops to boot + push time. Images are repo-specific: the hook
// is where a repo's toolchains (pnpm, python, ...) get baked in.
func (d *Driver) Bake(ctx context.Context, spec driver.BakeSpec) (string, error) {
	me, err := d.user(ctx)
	if err != nil {
		return "", err
	}
	cspec := driver.CreateSpec{Name: "bake", Repo: "bake", Branch: "bake"} // timeouts 0 = never park mid-bake
	id := instanceName("bake", me)
	pub, err := d.newKeypair(id)
	if err != nil {
		return "", err
	}
	defer os.Remove(d.keyPath(id))
	defer os.Remove(d.keyPath(id) + ".pub")

	work, err := os.MkdirTemp("", "pier-bake-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	udPath := filepath.Join(work, "user-data.yaml")
	if err := os.WriteFile(udPath, []byte(payload.RenderUserData(cspec, pub)), 0o600); err != nil {
		return "", err
	}

	// cspec.Image is empty, so launch always bakes from the stock Ubuntu image
	// (not a previous bake) — images don't accrete layers.
	if err := d.launch(ctx, cspec, me, id, pub, udPath); err != nil {
		return "", err
	}
	// The bake instance must never outlive this call, success or failure.
	// Destroy also drops the known_hosts entry — the next bake reuses this
	// deterministic name with a fresh host key.
	defer d.Destroy(context.WithoutCancel(ctx), id)
	fmt.Println(ui.Step("bake instance " + id + " launched — installing harnesses (a few minutes)"))

	if err := d.waitSSH(ctx, id, 300*time.Second); err != nil {
		return "", err
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
		return "", fmt.Errorf("harness install did not complete: %w", err)
	}
	if spec.HookPath != "" {
		fmt.Println(ui.Step("running .pier-bake.sh (output follows)"))
		if err := d.scpTo(ctx, id, spec.HookPath, "/tmp/pier-bake.sh", "-q"); err != nil {
			return "", err
		}
		if err := d.sshStream(ctx, id, "bash /tmp/pier-bake.sh && rm -f /tmp/pier-bake.sh"); err != nil {
			return "", fmt.Errorf(".pier-bake.sh failed — nothing baked: %w", err)
		}
	}
	// Per-instance state must not leak into the image.
	if _, err := d.sshRun(ctx, id, "rm -f ~/.ssh/authorized_keys && sudo rm -rf /etc/pier"); err != nil {
		return "", err
	}

	fmt.Println(ui.Step("imaging — stop, snapshot the boot disk (a few minutes)"))
	// Synchronous stop: images create wants the disk quiesced.
	if _, err := d.gcloud(ctx, "compute", "instances", "stop", id, "--zone", d.Zone); err != nil {
		return "", err
	}
	repo := payload.Sanitize(spec.RepoName)
	// Image names cap at 63 chars: "pier-" + repo + "-20060102-1504".
	if len(repo) > 44 {
		repo = strings.TrimRight(repo[:44], "-")
	}
	name := "pier-" + repo + "-" + time.Now().Format("20060102-1504")
	// The boot disk shares the instance's name; images create waits until the
	// image is ready.
	img, err := d.gcloud(ctx, "compute", "images", "create", name,
		"--source-disk", id, "--source-disk-zone", d.Zone,
		"--description", "pier session base for "+repo+" (harnesses + bake hook preinstalled)",
		"--labels", LabelManaged+"=1,"+MetaRepo+"="+repo,
		"--format", "value(name)")
	if err != nil {
		return "", err
	}

	for _, old := range spec.Replaces {
		if old != "" && old != img {
			_, _ = d.gcloud(ctx, "compute", "images", "delete", old)
		}
	}
	return img, nil
}

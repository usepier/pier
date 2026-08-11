package gcpgce

import (
	"context"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// Claim converts a warm pool member into a named session, labels and
// metadata only (GCE instances can't be renamed — the instance name is just
// an ID, and the placeholder name baked into it is cosmetic). Label writes
// are last-writer-wins, so ownership is settled by nonce read-back: write
// the nonce, wait a settle beat (covers a rival writing between our write
// and our read), read it back — the later writer wins and the earlier one
// sees a foreign nonce. The read-back also checks the pool label: removing
// it is a winner's commit point, so a rival that finished the race while we
// were writing leaves the member a live session our nonce must not hijack.
// Claimers also spread across members by nonce hash, so two concurrent
// claims rarely even meet on one instance.
func (d *Driver) Claim(ctx context.Context, id string, spec driver.ClaimSpec) error {
	if _, err := d.gcloud(ctx, "compute", "instances", "add-labels", id,
		"--zone", d.Zone, "--labels", LabelClaim+"="+spec.Nonce); err != nil {
		return err
	}
	time.Sleep(time.Second)
	out, err := d.gcloud(ctx, "compute", "instances", "describe", id,
		"--zone", d.Zone, "--format", "value(labels."+LabelClaim+",labels."+LabelPool+")")
	if err != nil {
		return err
	}
	// "<nonce>\t<gen>"; a missing label reads as empty.
	nonce, pool, _ := strings.Cut(out, "\t")
	if pool == "" {
		// Someone committed: the member is their session now. Our claim label
		// pollutes it — remove it (not value-scoped on GCE, but any claim
		// label on a committed member is leftover race debris regardless of
		// whose nonce it holds).
		_, _ = d.gcloud(ctx, "compute", "instances", "remove-labels", id,
			"--zone", d.Zone, "--labels", LabelClaim)
		return driver.ErrClaimLost
	}
	if nonce != spec.Nonce {
		return driver.ErrClaimLost
	}
	// add-metadata overwrites existing keys. MetaCreated starts AGE at claim:
	// creationTimestamp would show the member's fill time on a "new" session.
	// ValidateNames' charset keeps the comma-joined values safe.
	if _, err := d.gcloud(ctx, "compute", "instances", "add-metadata", id,
		"--zone", d.Zone, "--metadata",
		MetaSession+"="+spec.Name+","+MetaBranch+"="+spec.Branch+","+
			MetaCreated+"="+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	// Removing the pool label is the commit point: the member now lists as a
	// regular session.
	_, err = d.gcloud(ctx, "compute", "instances", "remove-labels", id,
		"--zone", d.Zone, "--labels", LabelPool+","+LabelClaim)
	return err
}

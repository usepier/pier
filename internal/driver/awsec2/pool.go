package awsec2

import (
	"context"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// Claim converts a warm pool member into a named session, tags only. EC2 tag
// writes have no compare-and-swap, so ownership is settled by nonce
// read-back: write the nonce, wait a settle beat (covers a rival writing
// between our write and our read), read it back — the later writer wins and
// the earlier one sees a foreign nonce. The read-back also checks the pool
// tag: deleting it is a winner's commit point, so a rival that finished the
// race while we were writing leaves the member a live session our nonce
// must not hijack. Claimers also spread across members by nonce hash, so two
// concurrent claims rarely even meet on one instance.
func (d *Driver) Claim(ctx context.Context, id string, spec driver.ClaimSpec) error {
	if _, err := d.aws(ctx, "ec2", "create-tags", "--resources", id,
		"--tags", "Key="+TagClaim+",Value="+spec.Nonce); err != nil {
		return err
	}
	time.Sleep(time.Second)
	out, err := d.aws(ctx, "ec2", "describe-tags",
		"--filters", "Name=resource-id,Values="+id, "Name=key,Values="+TagClaim+","+TagPool,
		"--query", "[Tags[?Key=='"+TagClaim+"'].Value|[0], Tags[?Key=='"+TagPool+"'].Value|[0]]",
		"--output", "text")
	if err != nil {
		return err
	}
	f := strings.Fields(out) // "<nonce> <gen>"; a missing tag reads as None
	if len(f) != 2 || f[1] == "None" {
		// Someone committed: the member is their session now. Our claim tag
		// pollutes it — delete it value-scoped (EC2 removes a Key+Value pair
		// only when the value still matches, so a third writer's nonce
		// survives us).
		_, _ = d.aws(ctx, "ec2", "delete-tags", "--resources", id,
			"--tags", "Key="+TagClaim+",Value="+spec.Nonce)
		return driver.ErrClaimLost
	}
	if f[0] != spec.Nonce {
		return driver.ErrClaimLost
	}
	// The created tag resets to now: the user's session begins at claim, and
	// AGE reads the created tag (fill time would show a days-old "new" session).
	if _, err := d.aws(ctx, "ec2", "create-tags", "--resources", id, "--tags",
		"Key=Name,Value=pier-"+spec.Name,
		"Key="+TagSession+",Value="+spec.Name,
		"Key="+TagBranch+",Value="+spec.Branch,
		"Key="+TagCreated+",Value="+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	// Deleting the pool tag is the commit point: the member now lists as a
	// regular session.
	_, err = d.aws(ctx, "ec2", "delete-tags", "--resources", id,
		"--tags", "Key="+TagPool, "Key="+TagClaim)
	return err
}

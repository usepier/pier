// Package pool implements repo-scoped warm session pools: pre-provisioned,
// parked instances whose repo checkout and .pier-setup.sh already ran, so a
// new session is a resume + freshen instead of a full create. Strictly
// opt-in (a pool exists only when the user sets a size) and driver-agnostic:
// everything here goes through the driver interface — members are ordinary
// instances plus one pool tag, and all pool state derives from provider
// tags/labels like everything else in pier. No daemon refills pools; fill
// runs when the user asks and detached after a claim consumes a member.
package pool

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/payload"
)

const (
	// FillLeash is the idle timeout members are created with: the supervisor
	// self-parks them shortly after setup finishes even when the laptop
	// running the fill dies first. Claim hands the user's real timeouts back.
	FillLeash = 2 * time.Minute
	// FillGrace is how long an unparked member may stay unparked before
	// reconcile treats it as a corpse: past it, either the fill died before
	// its member parked or setup is looping — a running instance nobody will
	// claim burns real money. Exported so doctor and the TUI census can flag
	// the same corpses reconcile would collect.
	FillGrace = 2 * time.Hour
	// setupWait bounds fill's wait for .pier-setup.sh. A setup this slow is
	// broken, not warm.
	setupWait = 45 * time.Minute
)

// Params carries everything fill and claim need, resolved by the cmd layer
// (config values, the repo's baked image, the driver's manifest/env) so this
// package stays config-agnostic.
type Params struct {
	Driver   driver.Driver
	RepoRoot string // local checkout; fill and claim run from inside it
	Gen      string // current generation (Generation(...))
	Size     int    // desired warm member count
	MaxAge   time.Duration
	Image    string // the repo's baked image; "" = stock

	// The user's configured timeouts: claim restores them (fill uses FillLeash).
	IdleTimeout   time.Duration
	UnattendedCap time.Duration

	Manifest   []string          // $HOME-relative files, same as the driver gets
	SessionEnv map[string]string // ~/.config/pier/env content, same as the driver gets
	Progress   func(string)
}

func (p *Params) progress() func(string) {
	if p.Progress == nil {
		return func(string) {}
	}
	return p.Progress
}

// Generation fingerprints everything that makes a warm member equivalent to
// a fresh create: a member built under a different driver, image, shape, or
// setup script is stale and gets recycled rather than claimed. 12 hex chars —
// lowercase, so it is valid as an EC2 tag value and a GCE label value alike.
func Generation(driverName, image, instanceType string, diskGiB int, setupScript []byte) string {
	setup := sha256.Sum256(setupScript)
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%d|%x", driverName, image, instanceType, diskGiB, setup)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// SetupScript returns the bytes of the setup script a session of this repo
// would run: the PIER_SETUP_SCRIPT override when set, else the repo's
// .pier-setup.sh, else nil. Feeds the generation fingerprint, and tells fill
// whether a setup status is expected at all.
func SetupScript(repoRoot string) []byte {
	p := os.Getenv("PIER_SETUP_SCRIPT")
	if p == "" {
		p = filepath.Join(repoRoot, ".pier-setup.sh")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// Members filters a List to the unclaimed pool members of one repo (basename).
func Members(sessions []driver.Session, repo string) []driver.Session {
	var out []driver.Session
	for _, s := range sessions {
		if s.PoolGen != "" && s.Repo == repo {
			out = append(out, s)
		}
	}
	return out
}

// Plan is Reconcile's verdict on one repo's pool.
type Plan struct {
	Ready    []driver.Session // parked, current gen, young enough — claimable, newest first
	InFlight int              // unparked but within the fill grace: a fill in progress
	Fill     int              // members to create
	Stale    []driver.Session // wrong gen, over age, dead, or grace-blown — destroy
}

// Reconcile classifies members against the desired state. Pure: callers pass
// time.Now(). Members mid-delete are ignored (they are going away on their
// own); when the pool holds more ready members than size wants, the oldest
// spill into Stale.
func Reconcile(members []driver.Session, size int, gen string, maxAge time.Duration, now time.Time) Plan {
	var p Plan
	for _, m := range members {
		age := now.Sub(m.Created)
		switch {
		case m.State == driver.StateDeleting:
		case m.PoolGen != gen, age >= maxAge, m.State == driver.StateDead:
			p.Stale = append(p.Stale, m)
		case m.State == driver.StateParked:
			p.Ready = append(p.Ready, m)
		case age < FillGrace:
			p.InFlight++
		default:
			p.Stale = append(p.Stale, m)
		}
	}
	slices.SortFunc(p.Ready, func(a, b driver.Session) int {
		return b.Created.Compare(a.Created)
	})
	if len(p.Ready) > size {
		// In-flight members don't count against ready ones here: a warm,
		// claimable member never drains in favor of an unfinished fill (which
		// may yet fail). Once that fill parks, the next reconcile trims the
		// brief overage — parked members cost pennies in the meantime.
		p.Stale = append(p.Stale, p.Ready[size:]...)
		p.Ready = p.Ready[:size]
	}
	if n := size - len(p.Ready) - p.InFlight; n > 0 {
		p.Fill = n
	}
	return p
}

// placeholderName names an unclaimed member: pool-<repo>-<rand4>. Passes the
// same name validation as user branches (Sanitize folds to its charset). The
// repo part is capped so the whole name stays inside GCE's 63-char instance
// name limit with room for the drivers' prefix — a long repo name must never
// squeeze out the random suffix that keeps members distinct.
func placeholderName(repoRoot string) (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	base := payload.Sanitize(filepath.Base(repoRoot))
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "-")
	}
	return "pool-" + base + "-" + hex.EncodeToString(b), nil
}

// newNonce mints a claim token: lowercase hex, so it is valid as an EC2 tag
// value and a GCE label value alike.
func newNonce() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// pickOrder returns candidate indices starting at a nonce-derived offset,
// then wrapping: two concurrent claimers usually go for different members,
// so the nonce read-back in Claim rarely even has a race to settle.
func pickOrder(n int, nonce string) []int {
	if n <= 0 {
		return nil
	}
	h := fnv.New32a()
	h.Write([]byte(nonce))
	start := int(h.Sum32() % uint32(n))
	out := make([]int, 0, n)
	for i := range n {
		out = append(out, (start+i)%n)
	}
	return out
}

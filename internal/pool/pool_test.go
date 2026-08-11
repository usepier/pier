package pool

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
	"github.com/kerem-kaynak/pier/internal/driver/payload"
)

// The generation is the staleness contract: any input that changes what a
// fresh create would produce must change it, and nothing else may.
func TestGeneration(t *testing.T) {
	base := Generation("aws-ec2", "ami-1", "t4g.medium", 40, []byte("npm ci"))
	if again := Generation("aws-ec2", "ami-1", "t4g.medium", 40, []byte("npm ci")); again != base {
		t.Errorf("generation is not stable: %s vs %s", base, again)
	}
	if len(base) != 12 || base != strings.ToLower(base) {
		t.Errorf("generation %q must be 12 lowercase hex chars (GCE label + EC2 tag safe)", base)
	}
	for name, gen := range map[string]string{
		"driver":  Generation("gcp-gce", "ami-1", "t4g.medium", 40, []byte("npm ci")),
		"image":   Generation("aws-ec2", "ami-2", "t4g.medium", 40, []byte("npm ci")),
		"type":    Generation("aws-ec2", "ami-1", "t4g.large", 40, []byte("npm ci")),
		"disk":    Generation("aws-ec2", "ami-1", "t4g.medium", 80, []byte("npm ci")),
		"setup":   Generation("aws-ec2", "ami-1", "t4g.medium", 40, []byte("npm ci && make")),
		"nosetup": Generation("aws-ec2", "ami-1", "t4g.medium", 40, nil),
	} {
		if gen == base {
			t.Errorf("changing %s did not change the generation", name)
		}
	}
}

func TestReconcile(t *testing.T) {
	now := time.Now()
	gen := "aaaaaaaaaaaa"
	maxAge := 14 * 24 * time.Hour
	member := func(id string, st driver.State, g string, age time.Duration) driver.Session {
		return driver.Session{ID: id, Name: id, State: st, PoolGen: g, Created: now.Add(-age)}
	}

	p := Reconcile([]driver.Session{
		member("ready-new", driver.StateParked, gen, time.Hour),
		member("ready-old", driver.StateParked, gen, 48*time.Hour),
		member("wrong-gen", driver.StateParked, "bbbbbbbbbbbb", time.Hour),
		member("over-age", driver.StateParked, gen, maxAge+time.Hour),
		member("filling", driver.StateCreating, gen, 5*time.Minute),
		member("grace-blown", driver.StateRunning, gen, 3*time.Hour),
		member("dead", driver.StateDead, gen, time.Hour),
		member("going-away", driver.StateDeleting, gen, time.Hour),
	}, 5, gen, maxAge, now)

	ready := ids(p.Ready)
	if want := []string{"ready-new", "ready-old"}; !slices.Equal(ready, want) {
		t.Errorf("Ready = %v, want %v (newest first)", ready, want)
	}
	if p.InFlight != 1 {
		t.Errorf("InFlight = %d, want 1", p.InFlight)
	}
	stale := ids(p.Stale)
	slices.Sort(stale)
	if want := []string{"dead", "grace-blown", "over-age", "wrong-gen"}; !slices.Equal(stale, want) {
		t.Errorf("Stale = %v, want %v", stale, want)
	}
	// 5 wanted - 2 ready - 1 in flight
	if p.Fill != 2 {
		t.Errorf("Fill = %d, want 2", p.Fill)
	}
}

// Shrinking the pool must drain the oldest ready members, never the in-flight
// ones (they belong to a live fill whose failure path destroys them itself).
func TestReconcileShrink(t *testing.T) {
	now := time.Now()
	gen := "aaaaaaaaaaaa"
	members := []driver.Session{
		{ID: "old", Name: "old", State: driver.StateParked, PoolGen: gen, Created: now.Add(-3 * time.Hour)},
		{ID: "new", Name: "new", State: driver.StateParked, PoolGen: gen, Created: now.Add(-time.Hour)},
		{ID: "filling", Name: "filling", State: driver.StateCreating, PoolGen: gen, Created: now.Add(-time.Minute)},
	}
	p := Reconcile(members, 1, gen, 14*24*time.Hour, now)
	if want := []string{"new", "old"}; !slices.Equal(ids(p.Stale), []string{"old"}) || !slices.Equal(ids(p.Ready), want[:1]) {
		t.Errorf("shrink to 1: Ready = %v, Stale = %v; want Ready [new], Stale [old]", ids(p.Ready), ids(p.Stale))
	}
	if p.Fill != 0 {
		t.Errorf("Fill = %d, want 0 (ready + in-flight covers the size)", p.Fill)
	}
	if p := Reconcile(members, 0, gen, 14*24*time.Hour, now); len(p.Ready) != 0 || len(p.Stale) != 2 {
		t.Errorf("shrink to 0: Ready = %v, Stale = %v; want all ready drained", ids(p.Ready), ids(p.Stale))
	}
}

func ids(members []driver.Session) []string {
	var out []string
	for _, m := range members {
		out = append(out, m.ID)
	}
	return out
}

// pickOrder spreads concurrent claimers across members but must still visit
// every candidate, or a lost race would strand claimable members.
func TestPickOrder(t *testing.T) {
	order := pickOrder(5, "abc123")
	if len(order) != 5 {
		t.Fatalf("pickOrder(5) visits %d members, want 5", len(order))
	}
	sorted := slices.Clone(order)
	slices.Sort(sorted)
	if !slices.Equal(sorted, []int{0, 1, 2, 3, 4}) {
		t.Errorf("pickOrder(5) = %v, want a permutation-by-rotation of 0..4", order)
	}
	if !slices.Equal(order, pickOrder(5, "abc123")) {
		t.Error("pickOrder is not deterministic for one nonce")
	}
	if pickOrder(0, "abc123") != nil {
		t.Error("pickOrder(0) must be nil")
	}
}

// The placeholder rides everywhere a session name does (tags, hostnames,
// bootstrap splices), so it must pass the same validation.
func TestPlaceholderName(t *testing.T) {
	name, err := placeholderName("/Users/x/My_Repo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "pool-my-repo-") {
		t.Errorf("placeholderName = %q, want pool-my-repo-<rand> (Sanitize folds the basename)", name)
	}
	spec := driver.CreateSpec{Name: name, Branch: name, Repo: "/Users/x/My_Repo"}
	if err := payload.ValidateNames(spec); err != nil {
		t.Errorf("placeholder %q fails name validation: %v", name, err)
	}
	other, _ := placeholderName("/Users/x/My_Repo")
	if name == other {
		t.Errorf("two placeholders collided: %q", name)
	}
}

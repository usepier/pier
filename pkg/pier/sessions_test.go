package pier

import (
	"os/exec"
	"testing"
	"time"

	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/internal/tombstone"
)

// A failed create used to vanish without trace: the instance rolled itself
// back and nothing was left to say a session had been asked for at all. The
// tombstone rides the list so the failure is visible — but only until the
// cloud can answer for that name itself.
func TestMergeTombstones(t *testing.T) {
	live := []driver.Session{{Name: "kur-3814", State: driver.StateIdle}}
	recs := []tombstone.Record{
		{Name: "kur-3817", Repo: "flb", Reason: "scp: Connection closed", LogPath: "/tmp/c.log"},
		{Name: "kur-3814", Reason: "an earlier attempt that has since worked"},
	}

	merged, revived := mergeTombstones(live, recs)
	if len(merged) != 2 {
		t.Fatalf("want the live session plus one tombstone, got %+v", merged)
	}
	if merged[0].Name != "kur-3814" || merged[0].State != driver.StateIdle {
		t.Errorf("a live session must survive the merge untouched, got %+v", merged[0])
	}
	got := merged[1]
	if got.Name != "kur-3817" || got.State != driver.StateFailed {
		t.Errorf("want kur-3817 as a failed row, got %+v", got)
	}
	if got.FailReason == "" || got.LogPath != "/tmp/c.log" {
		t.Errorf("a tombstone must carry why it died and where to read more, got %+v", got)
	}
	if got.CostNote != "—" {
		t.Errorf("nothing is running, so nothing is billing; got CostNote %q", got.CostNote)
	}
	// The obvious next action — re-create it — must clear the row by itself.
	if len(revived) != 1 || revived[0] != "kur-3814" {
		t.Errorf("a tombstone whose name came back live must be reported stale, got %v", revived)
	}
}

func TestRetryAttachOnlyForQuickSSHTransportFailure(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	if !RetryAttach(exit("255"), time.Second) {
		t.Error("quick ssh transport failure should retry")
	}
	if RetryAttach(exit("1"), time.Second) {
		t.Error("remote command failure should not retry")
	}
	if RetryAttach(exit("255"), 16*time.Second) {
		t.Error("slow transport failure should not retry")
	}
}

// FoldReady classifies members the way reconcile would; the generation check
// applies only to the repo whose local checkout produced curGen — other
// repos' gens are unknowable here.
func TestFoldReady(t *testing.T) {
	now := time.Now()
	ss := []Session{
		{Repo: "shop", PoolGen: "aaa", State: StateParked, Created: now.Add(-time.Hour)},
		{Repo: "shop", PoolGen: "bbb", State: StateParked, Created: now}, // wrong gen
		{Repo: "shop", PoolGen: "aaa", State: StateCreating, Created: now},
		{Repo: "shop", PoolGen: "aaa", State: StateDead, Created: now},
		{Repo: "shop", PoolGen: "aaa", State: StateDeleting, Created: now},
		{Repo: "shop", State: StateRunning, Created: now}, // a real session
		{Repo: "tools", PoolGen: "zzz", State: StateParked, Created: now},
	}
	if st := FoldReady(ss, "shop", "shop", "aaa", 0, now); st.Ready != 1 || st.Filling != 1 || st.Stale != 2 {
		t.Errorf("shop = %+v, want 1 ready, 1 filling, 2 stale", st)
	}
	if st := FoldReady(ss, "tools", "shop", "aaa", 0, now); st.Ready != 1 || st.Stale != 0 {
		t.Errorf("tools = %+v, want its member ready (gen unknowable from here)", st)
	}

	// Age judgments: a parked member past max_age is stale even for repos
	// whose gen is unknowable, and an unparked member past the fill grace is
	// a corpse, not "filling" — its fill died hours ago.
	old := []Session{
		{Repo: "tools", PoolGen: "zzz", State: StateParked, Created: now.Add(-30 * 24 * time.Hour)},
		{Repo: "tools", PoolGen: "zzz", State: StateCreating, Created: now.Add(-3 * time.Hour)},
	}
	if st := FoldReady(old, "tools", "", "", 14*24*time.Hour, now); st.Ready != 0 || st.Filling != 0 || st.Stale != 2 {
		t.Errorf("old members = %+v, want both stale (over-age + grace-blown)", st)
	}
	if st := FoldReady(old[:1], "tools", "", "", 0, now); st.Ready != 1 {
		t.Errorf("maxAge 0 = %+v, want the age check skipped", st)
	}
}

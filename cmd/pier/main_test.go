package main

import (
	"os/exec"
	"strings"
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

	merged, revived := mergeTombstones(live, live, recs, "alice")
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

// "create failed" and "setup failed" sit in the same column and mean opposite
// things: one has no VM at all, the other has a running one whose setup
// script died. They must never render as the same word.
func TestStateLabelDistinguishesCreateFromSetupFailure(t *testing.T) {
	failed := stateLabel(driver.Session{State: driver.StateFailed})
	if failed != "create failed" {
		t.Errorf(`want "create failed", got %q`, failed)
	}
	setup := stateLabel(driver.Session{State: driver.StateIdle, Setup: "failed"})
	if !strings.Contains(setup, "idle") || !strings.Contains(setup, "setup failed") {
		t.Errorf("a live session with a dead setup script must still read as running, got %q", setup)
	}
}

func TestRetryAttachOnlyForQuickSSHTransportFailure(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	if !retryAttach(exit("255"), time.Second) {
		t.Error("quick ssh transport failure should retry")
	}
	if retryAttach(exit("1"), time.Second) {
		t.Error("remote command failure should not retry")
	}
	if retryAttach(exit("255"), 16*time.Second) {
		t.Error("slow transport failure should not retry")
	}
}

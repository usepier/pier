package pier

import (
	"encoding/json"
	"errors"
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

// FoldPool classifies members the way reconcile would; the generation check
// applies only to the repo whose local checkout produced curGen — other
// repos' gens are unknowable here.
func TestFoldPool(t *testing.T) {
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
	if st := FoldPool(ss, "shop", "shop", "aaa", 0, now); st.Ready != 1 || st.Filling != 1 || st.Stale != 2 {
		t.Errorf("shop = %+v, want 1 ready, 1 filling, 2 stale", st)
	}
	if st := FoldPool(ss, "tools", "shop", "aaa", 0, now); st.Ready != 1 || st.Stale != 0 {
		t.Errorf("tools = %+v, want its member ready (gen unknowable from here)", st)
	}

	// Age judgments: a parked member past max_age is stale even for repos
	// whose gen is unknowable, and an unparked member past the fill grace is
	// a corpse, not "filling" — its fill died hours ago.
	old := []Session{
		{Repo: "tools", PoolGen: "zzz", State: StateParked, Created: now.Add(-30 * 24 * time.Hour)},
		{Repo: "tools", PoolGen: "zzz", State: StateCreating, Created: now.Add(-3 * time.Hour)},
	}
	if st := FoldPool(old, "tools", "", "", 14*24*time.Hour, now); st.Ready != 0 || st.Filling != 0 || st.Stale != 2 {
		t.Errorf("old members = %+v, want both stale (over-age + grace-blown)", st)
	}
	if st := FoldPool(old[:1], "tools", "", "", 0, now); st.Ready != 1 {
		t.Errorf("maxAge 0 = %+v, want the age check skipped", st)
	}
}

// The connecting screen must not change what the attach exits with: the
// one bounded retry keys off ssh's 255.
func TestConnectingScreenKeepsTheExitStatus(t *testing.T) {
	for _, restore := range []bool{true, false} {
		cmd := ConnectingScreen(exec.Command("sh", "-c", "exit 255"), "s", restore)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 {
			t.Errorf("restore=%v: want exit 255 through the wrapper, got %v", restore, err)
		}
	}
	out, err := ConnectingScreen(exec.Command("echo", "hi"), "demo", true).Output()
	if err != nil || !strings.Contains(string(out), "connecting to demo") || !strings.Contains(string(out), "hi") {
		t.Errorf("want the connecting line then the command's output, got %q %v", out, err)
	}
}

// A beacon from a current supervisor carries load, port owners and the
// park countdown; one from an older supervisor carries none of it and
// leaves them zero.
func TestBeaconCarriesLoadAndCountdown(t *testing.T) {
	var b beacon
	in := `{"state":"idle","res":{"cpu":34.5,"cpus":2,"mem_used":1288490188,"mem_total":4294967296,"disk_used":12884901888,"disk_total":42949672960,"load1":0.5},
		"ports":[{"port":3000,"process":"node","pid":12}],"park_at":"2026-10-08T12:12:00Z"}`
	if err := json.Unmarshal([]byte(in), &b); err != nil {
		t.Fatal(err)
	}
	var s Session
	b.apply(&s)
	if s.Res == nil || s.Res.CPU != 34.5 || len(s.Ports) != 1 || s.Ports[0].Process != "node" {
		t.Fatalf("session = %+v", s)
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-08T12:00:00Z")
	if got := Countdown(s, now); got != "parks in 12m" {
		t.Errorf("Countdown = %q", got)
	}
	if got := Load(s); got != "cpu 34% · mem 1.2/4.0 GiB · disk 12/40 GiB" && got != "cpu 35% · mem 1.2/4.0 GiB · disk 12/40 GiB" {
		t.Errorf("Load = %q", got)
	}
	var old beacon
	json.Unmarshal([]byte(`{"state":"idle"}`), &old)
	var s2 Session
	old.apply(&s2)
	if s2.Res != nil || Countdown(s2, now) != "" || Load(s2) != "" {
		t.Error("an older beacon carries no load or countdown")
	}
}

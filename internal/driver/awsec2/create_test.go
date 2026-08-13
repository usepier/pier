package awsec2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/usepier/pier/internal/driver"
)

// A create that dies here destroys its instance, so the push is the one
// place where a blip on the laptop's network costs a whole VM. It must
// survive one — and it must not paper over a failure that retrying can't fix.
func TestPushRetriesBrokenTransportOnly(t *testing.T) {
	swap(t, &pushBackoff, time.Millisecond)

	t.Run("recovers from a broken connection", func(t *testing.T) {
		d, calls := newPushDriver(t)
		d.scpFn = func(_ context.Context, _, _, _ string) error {
			*calls++
			if *calls == 1 {
				return errors.New("ssh: connect to host 3.78.221.163 port 22: Network is unreachable")
			}
			return nil
		}
		if err := d.push(context.Background(), "i-0abc", "/tmp/x", "/tmp/x", nil); err != nil {
			t.Fatalf("a recovered transport blip must not fail the push: %v", err)
		}
		if *calls != 2 {
			t.Errorf("want a retry, got %d attempt(s)", *calls)
		}
		// The retry must have been steered off the path that just broke.
		if p, ok := d.dprobe["i-0abc"]; !ok || p.ip != "" {
			t.Errorf("a broken transport must demote the instance to the tunnel, got %+v", p)
		}
	})

	t.Run("gives up on a persistent break", func(t *testing.T) {
		d, calls := newPushDriver(t)
		d.scpFn = func(_ context.Context, _, _, _ string) error {
			*calls++
			return errors.New("scp: Connection closed")
		}
		if err := d.push(context.Background(), "i-0abc", "/tmp/x", "/tmp/x", nil); err == nil {
			t.Fatal("a push that never lands must fail the create")
		}
		if *calls != 3 {
			t.Errorf("want 3 bounded attempts, got %d", *calls)
		}
	})

	t.Run("does not retry a refusal", func(t *testing.T) {
		d, calls := newPushDriver(t)
		d.scpFn = func(_ context.Context, _, _, _ string) error {
			*calls++
			return errors.New("scp: /tmp/x: Permission denied")
		}
		if err := d.push(context.Background(), "i-0abc", "/tmp/x", "/tmp/x", nil); err == nil {
			t.Fatal("want the refusal surfaced")
		}
		if *calls != 1 {
			t.Errorf("a remote refusal fails identically on a retry; want 1 attempt, got %d", *calls)
		}
		if _, demoted := d.dprobe["i-0abc"]; demoted {
			t.Error("a remote refusal says nothing about the transport — it must not demote")
		}
	})

	t.Run("stops when the user cancels", func(t *testing.T) {
		d, calls := newPushDriver(t)
		ctx, cancel := context.WithCancel(context.Background())
		d.scpFn = func(_ context.Context, _, _, _ string) error {
			*calls++
			cancel()
			return errors.New("scp: Connection closed")
		}
		if err := d.push(ctx, "i-0abc", "/tmp/x", "/tmp/x", nil); err == nil {
			t.Fatal("a cancelled push must report the failure")
		}
		if *calls != 1 {
			t.Errorf("ctrl-c is a decision, not a blip; want 1 attempt, got %d", *calls)
		}
	})
}

func newPushDriver(t *testing.T) (*Driver, *int) {
	t.Helper()
	return &Driver{StateDir: t.TempDir(), Direct: true}, new(int)
}

func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// AGE reads the created tag: launch time resets on every resume and the
// beacon's state changes are even noisier, so create time must ride a tag.
func TestTagListCarriesCreateTime(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: "/tmp/myrepo", Branch: "main"}
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] != TagCreated {
			continue
		}
		if _, err := time.Parse(time.RFC3339, tag["Value"]); err != nil {
			t.Fatalf("%s value %q is not RFC3339: %v", TagCreated, tag["Value"], err)
		}
		return
	}
	t.Fatalf("tagList missing %s", TagCreated)
}

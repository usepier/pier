package awsec2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// The pool tag is what separates a warm member from a user session in every
// view — a regular create must never carry it, a pool create always must.
func TestTagListPoolGen(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: "/tmp/myrepo", Branch: "main"}
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] == TagPool {
			t.Fatalf("regular create carries %s=%q", TagPool, tag["Value"])
		}
	}
	spec.PoolGen = "ab12cd34ef56"
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] == TagPool {
			if tag["Value"] != spec.PoolGen {
				t.Fatalf("%s = %q, want %q", TagPool, tag["Value"], spec.PoolGen)
			}
			return
		}
	}
	t.Fatalf("pool create missing %s", TagPool)
}

// fakeAWS puts an aws CLI on PATH that answers the calls launch makes and
// logs every run-instances invocation's arguments, one line each. With
// rejectInitRate it fails the way a CLI older than the field does.
func fakeAWS(t *testing.T, rejectInitRate bool) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "run-instances.log")
	reject := ""
	if rejectInitRate {
		reject = `case "$*" in *VolumeInitializationRate*) echo 'Parameter validation failed: Unknown parameter in BlockDeviceMappings[0].Ebs: "VolumeInitializationRate"' >&2; exit 252;; esac`
	}
	script := "#!/bin/sh\ncase \"$2\" in\n" +
		"describe-vpcs) echo vpc-1 ;;\n" +
		"describe-security-groups) echo sg-1 ;;\n" +
		"describe-images) echo /dev/sda1 ;;\n" +
		"run-instances) echo \"$*\" >> " + log + "\n" + reject + "\necho i-new ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return log
}

// A prebuilt image is gigabytes of lazily-loaded snapshot: baked launches
// ask EBS to hydrate the volume up front, stock ones (small, AWS-cached)
// don't — and a CLI too old for the field must cost speed, never the launch.
func TestLaunchVolumeInitialization(t *testing.T) {
	d := &Driver{InstanceType: "t4g.medium", DiskGiB: 40}
	ud := filepath.Join(t.TempDir(), "ud")
	os.WriteFile(ud, nil, 0o600)
	spec := driver.CreateSpec{Name: "s", Repo: "/tmp/r", Branch: "s"}

	t.Run("baked asks for provisioned-rate hydration", func(t *testing.T) {
		log := fakeAWS(t, false)
		spec := spec
		spec.Image = "ami-baked"
		if _, err := d.launch(context.Background(), spec, "me", spec.Image, ud); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(log)
		if !strings.Contains(string(b), `"VolumeInitializationRate":300`) {
			t.Errorf("baked launch must request volume initialization, ran: %s", b)
		}
	})

	t.Run("stock does not", func(t *testing.T) {
		log := fakeAWS(t, false)
		if _, err := d.launch(context.Background(), spec, "me", "ami-stock", ud); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(log); strings.Contains(string(b), "VolumeInitializationRate") {
			t.Errorf("stock launch must not pay for hydration, ran: %s", b)
		}
	})

	t.Run("old CLI falls back without it", func(t *testing.T) {
		log := fakeAWS(t, true)
		spec := spec
		spec.Image = "ami-baked"
		var notes []string
		spec.Progress = func(s string) { notes = append(notes, s) }
		id, err := d.launch(context.Background(), spec, "me", spec.Image, ud)
		if err != nil || id != "i-new" {
			t.Fatalf("launch must survive a CLI that predates the field: id=%q err=%v", id, err)
		}
		b, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) != 2 || strings.Contains(lines[1], "VolumeInitializationRate") {
			t.Errorf("want one rejected attempt then one without the field, ran:\n%s", b)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "upgrade") {
			t.Errorf("the fallback must say how to get the speed back, got %q", notes)
		}
	})
}

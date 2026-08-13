package awsec2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The transport choice is the whole point of direct connect: with a live
// probe result the opts must dial the public IP (keyed by instance id in
// known_hosts) and drop both the ProxyCommand and the tunnel-only -C; with
// direct off — or the probe negative — they must ride SSM.
func TestSSHOptsTransports(t *testing.T) {
	d := &Driver{StateDir: t.TempDir()}
	opts := strings.Join(d.sshOpts(context.Background(), "i-0abc"), " ")
	if !strings.Contains(opts, "ProxyCommand=aws ssm start-session") || !strings.Contains(opts, "-C") {
		t.Errorf("direct off must use the SSM tunnel with compression, got %q", opts)
	}

	d = &Driver{
		StateDir: t.TempDir(),
		Direct:   true,
		dprobe: map[string]directProbe{
			"i-0abc": {ip: "203.0.113.7", until: time.Now().Add(time.Minute)},
		},
	}
	opts = strings.Join(d.sshOpts(context.Background(), "i-0abc"), " ")
	if !strings.Contains(opts, "HostName=203.0.113.7") || !strings.Contains(opts, "HostKeyAlias=i-0abc") {
		t.Errorf("direct on must dial the public IP keyed by instance id, got %q", opts)
	}
	if strings.Contains(opts, "ProxyCommand") || strings.Contains(opts, "-C") {
		t.Errorf("direct path must not carry SSM options, got %q", opts)
	}

	// A negative probe (cached "") means the tunnel, without re-probing.
	d.dprobe["i-0abc"] = directProbe{ip: "", until: time.Now().Add(time.Minute)}
	opts = strings.Join(d.sshOpts(context.Background(), "i-0abc"), " ")
	if !strings.Contains(opts, "ProxyCommand") {
		t.Errorf("negative probe must fall back to the SSM tunnel, got %q", opts)
	}
}

func TestAttachCommandFallsBackForUnknownTerminal(t *testing.T) {
	d := &Driver{StateDir: t.TempDir()}
	cmd, err := d.AttachCommand(context.Background(), "i-0abc")
	if err != nil {
		t.Fatal(err)
	}
	remote := cmd.Args[len(cmd.Args)-1]
	if !strings.Contains(remote, `infocmp "$TERM"`) || !strings.Contains(remote, "export TERM=xterm-256color") {
		t.Errorf("attach must fall back when the VM lacks the client's terminfo entry, got %q", remote)
	}
}

// Park, resume and resize invalidate the probe cache — a cached success
// otherwise points connections at the old public IP for up to a TTL after
// the instance comes back on a new one.
func TestDropProbe(t *testing.T) {
	d := &Driver{
		Direct: true,
		dprobe: map[string]directProbe{
			"i-0abc": {ip: "203.0.113.7", until: time.Now().Add(time.Minute)},
		},
	}
	d.dropProbe("i-0abc")
	if _, ok := d.dprobe["i-0abc"]; ok {
		t.Error("dropProbe must forget the cached probe")
	}
	d.dropProbe("i-0abc") // absent id is a no-op, not a panic
}

// The regression that cost two sessions: a transfer died on the direct path,
// and the retry re-read the still-valid probe cache, dialed the same dead
// address, and failed the create. Demotion must park the instance on the
// tunnel so the retry actually takes a different route.
func TestDemoteDirectSendsRetryThroughTunnel(t *testing.T) {
	d := &Driver{
		StateDir: t.TempDir(),
		Direct:   true,
		dprobe: map[string]directProbe{
			"i-0abc": {ip: "203.0.113.7", until: time.Now().Add(time.Minute)},
		},
	}
	if opts := strings.Join(d.sshOpts(context.Background(), "i-0abc"), " "); !strings.Contains(opts, "HostName=203.0.113.7") {
		t.Fatalf("precondition: a live probe should dial direct, got %q", opts)
	}
	d.demoteDirect("i-0abc")
	opts := strings.Join(d.sshOpts(context.Background(), "i-0abc"), " ")
	if !strings.Contains(opts, "ProxyCommand=aws ssm start-session") {
		t.Errorf("a demoted instance must ride the tunnel, got %q", opts)
	}
	if strings.Contains(opts, "HostName=203.0.113.7") {
		t.Errorf("a demoted instance must not re-dial the address that just failed, got %q", opts)
	}
	// Demotion is temporary: it expires so a healed network goes fast again.
	if p := d.dprobe["i-0abc"]; p.until.After(time.Now().Add(directDemotion + time.Minute)) {
		t.Errorf("demotion must expire, got until=%v", p.until)
	}
	// An instance never probed before can still be demoted (nil-map safety).
	(&Driver{}).demoteDirect("i-0new")
}

// Only a broken connection says anything about which path to use next. A
// remote command that exits nonzero would do so identically over the tunnel,
// so retrying it there is pointless — and demoting on it would drag every
// session onto the slow path for ordinary script failures.
func TestTransportBroken(t *testing.T) {
	for in, want := range map[string]bool{
		"ssh: connect to host 3.78.221.163 port 22: Network is unreachable": true,
		"scp: Connection closed":                                     true,
		"client_loop: send disconnect: Broken pipe":                  true,
		"ssh: connect to host 10.0.0.1 port 22: Operation timed out": true,
		// waitSSH has already seen sshd answer, so a refusal during the push
		// is a bounced service, not a shut port — probeDirect reads it the
		// same way, and destroying an instance over it would be absurd.
		"ssh: connect to host 10.0.0.1 port 22: Connection refused": true,
		"no route to host":               true,
		"bootstrap: exit status 1":       false,
		"scp: /tmp/x: Permission denied": false,
		"Error 127: pnpm: No such file":  false,
		"":                               false,
	} {
		if got := transportBroken(errors.New(in)); got != want {
			t.Errorf("transportBroken(%q) = %v, want %v", in, got, want)
		}
	}
	if transportBroken(nil) {
		t.Error("transportBroken(nil) must be false")
	}
}

func TestSanitizeRuleName(t *testing.T) {
	for in, want := range map[string]string{
		"kerem":          "kerem",
		"dev role/x y":   "dev-role-x-y",
		"a.b_c-d@e":      "a.b_c-d@e",
		"":               "unknown",
		"héllo":          "h-llo",
		"user+tag=extra": "user-tag-extra",
	} {
		if got := sanitizeRuleName(in); got != want {
			t.Errorf("sanitizeRuleName(%q) = %q, want %q", in, got, want)
		}
	}
}
